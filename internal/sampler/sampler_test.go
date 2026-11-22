package sampler

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cleonte/go-packet/internal/clock"
	"github.com/cleonte/go-packet/internal/num"
	"github.com/cleonte/go-packet/internal/probe"
	"github.com/cleonte/go-packet/internal/probe/probetest"
	"github.com/cleonte/go-packet/internal/store"
	"github.com/cleonte/go-packet/internal/verdict"
)

func TestBandwidthSchedule(t *testing.T) {
	const m = 60_000
	now := int64(1000 * m)
	minsAgo := func(min int64) *int64 { v := now - min*m; return &v }
	check := func(name string, got probe.Trigger, ok bool, want probe.Trigger, wantOK bool) {
		t.Helper()
		if ok != wantOK || (ok && got != want) {
			t.Errorf("%s: got (%v,%v) want (%v,%v)", name, got, ok, want, wantOK)
		}
	}
	full := minsAgo(20)
	g, f := verdict.Good, verdict.Fair
	tr, ok := bandwidthDue(now, nil, nil, g, g, false, true)
	check("first round full", tr, ok, probe.TrigHourly, true)
	tr, ok = bandwidthDue(now, minsAgo(5), full, g, g, false, true)
	check("nothing due", tr, ok, 0, false)
	tr, ok = bandwidthDue(now, minsAgo(10), full, g, g, false, true)
	check("quick due", tr, ok, probe.TrigScheduled, true)
	tr, ok = bandwidthDue(now, minsAgo(4), full, f, g, false, true)
	check("turned bad", tr, ok, probe.TrigBadRound, true)
	tr, ok = bandwidthDue(now, minsAgo(2), full, verdict.Poor, g, false, true)
	check("rate limited", tr, ok, 0, false)
	tr, ok = bandwidthDue(now, minsAgo(4), full, f, f, false, true)
	check("staying bad", tr, ok, 0, false)
	tr, ok = bandwidthDue(now, nil, nil, g, g, false, false)
	check("hourly off", tr, ok, probe.TrigScheduled, true)
	tr, ok = bandwidthDue(now, nil, nil, g, g, true, true)
	check("expensive path", tr, ok, 0, false)
	tr, ok = bandwidthDue(now, nil, nil, verdict.Offline, g, false, true)
	check("offline", tr, ok, 0, false)
}

func TestHourlyMarkerIgnoresLinks(t *testing.T) {
	dir := t.TempDir()
	if hourlyEnabledIn(dir) {
		t.Fatal("off by default")
	}
	target := filepath.Join(dir, "target")
	_ = os.WriteFile(target, []byte("keep"), 0o600)
	_ = os.Symlink(target, filepath.Join(dir, hourlyMarker))
	if hourlyEnabledIn(dir) {
		t.Fatal("a planted link must not turn it on")
	}
	if err := setHourlyIn(dir, true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Fatal("link target was written through")
	}
	if !hourlyEnabledIn(dir) {
		t.Fatal("on")
	}
	_ = setHourlyIn(dir, false)
	if hourlyEnabledIn(dir) {
		t.Fatal("off")
	}
}

func TestStepFlow(t *testing.T) {
	db, _ := store.OpenAt(filepath.Join(t.TempDir(), "s.sqlite"))
	defer db.Close()
	st := loadState(db, clock.NowMs())
	now := clock.NowMs()
	st.lastQuick = &probe.Bandwidth{AtMs: now, Trigger: probe.TrigScheduled}
	st.lastFull = &probe.Bandwidth{AtMs: now, Trigger: probe.TrigHourly}
	measure := func(at int64, _ uint64) *probe.Round { r := probetest.Healthy(); r.AtMs = at; return r }
	bw := func(tr probe.Trigger, at int64) probe.Bandwidth {
		return probe.Bandwidth{AtMs: at, Trigger: tr, DownMbps: num.Ptr(300), UpMbps: num.Ptr(200), DownBytes: 1, UpBytes: 1}
	}
	var seen [][2]bool
	notify := func(s Snapshot) { seen = append(seen, [2]bool{s.Testing, s.ManualDone}) }
	step(db, st, false, false, measure, bw, notify)
	if len(seen) != 1 || seen[0] != [2]bool{false, false} {
		t.Fatalf("fresh tests: no bandwidth, got %v", seen)
	}
	time.Sleep(5 * time.Millisecond)
	seen = nil
	step(db, st, true, false, measure, bw, notify)
	if len(seen) != 2 || seen[0] != [2]bool{true, false} || seen[1] != [2]bool{false, true} {
		t.Fatalf("Test Now reports testing then the result, got %v", seen)
	}
	var manual int
	_ = db.QueryRow("SELECT COUNT(*) FROM bandwidth WHERE trigger = 2").Scan(&manual)
	if manual != 1 {
		t.Fatal("manual test stored", manual)
	}
}

func TestIsWake(t *testing.T) {
	const m = 60_000
	if IsWake(0, 10*m) {
		t.Fatal("no previous round is not a wake")
	}
	if IsWake(10*m, 11*m) || IsWake(10*m, 12*m) {
		t.Fatal("an ordinary or one-missed round is not a wake")
	}
	if !IsWake(10*m, 10*m+wakeGapMs+1) {
		t.Fatal("a long gap is a wake")
	}
}

func TestRetryAfterWakeIgnoresTheFirstBadRound(t *testing.T) {
	old := retryDelay
	retryDelay = 0
	defer func() { retryDelay = old }()
	db, _ := store.OpenAt(filepath.Join(t.TempDir(), "w.sqlite"))
	defer db.Close()
	st := loadState(db, clock.NowMs())
	calls := 0
	measure := func(at int64, _ uint64) *probe.Round {
		calls++
		if calls == 1 {
			return &probe.Round{AtMs: at, Link: probe.LinkNone} // the link is not back yet
		}
		r := probetest.Healthy()
		r.AtMs = at
		return r
	}
	var last Snapshot
	step(db, st, false, true, measure, func(probe.Trigger, int64) probe.Bandwidth { return probe.Bandwidth{} },
		func(s Snapshot) { last = s })
	if calls != 2 || last.Verdict.Level != verdict.Good {
		t.Fatalf("calls=%d verdict=%v: the retry must replace the bad round", calls, last.Verdict)
	}
	rounds, _ := store.RoundsBetween(db, 0, clock.NowMs()+1000)
	if len(rounds) != 1 || rounds[0].Verdict.Level != verdict.Good {
		t.Fatalf("only the second round is stored, got %d", len(rounds))
	}

	// Without the flag, an offline round counts at once.
	calls = 0
	st = loadState(db, clock.NowMs())
	offline := func(at int64, _ uint64) *probe.Round {
		calls++
		return &probe.Round{AtMs: at + 5, Link: probe.LinkNone}
	}
	step(db, st, false, false, offline, func(probe.Trigger, int64) probe.Bandwidth { return probe.Bandwidth{} },
		func(s Snapshot) { last = s })
	if calls != 1 || last.Verdict.Level != verdict.Offline {
		t.Fatalf("calls=%d verdict=%v", calls, last.Verdict)
	}
}

func TestWatchPathKicksOnChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var gen atomic.Int32
	kick := make(chan struct{}, 1)
	go watchPath(ctx, func() int { return int(gen.Load()) }, 5*time.Millisecond, kick)
	select {
	case <-kick:
		t.Fatal("no change, no kick")
	case <-time.After(40 * time.Millisecond):
	}
	gen.Add(1)
	select {
	case <-kick:
	case <-time.After(time.Second):
		t.Fatal("a path change must kick")
	}
	gen.Add(1)
	gen.Add(1) // a burst while a kick is pending collapses into one
	time.Sleep(40 * time.Millisecond)
	if len(kick) != 1 {
		t.Fatalf("pending kicks = %d, want 1", len(kick))
	}
}
