package sampler

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cleonte/menubar/packet-go/internal/clock"
	"github.com/cleonte/menubar/packet-go/internal/num"
	"github.com/cleonte/menubar/packet-go/internal/probe"
	"github.com/cleonte/menubar/packet-go/internal/probe/probetest"
	"github.com/cleonte/menubar/packet-go/internal/store"
	"github.com/cleonte/menubar/packet-go/internal/verdict"
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
	step(db, st, false, measure, bw, notify)
	if len(seen) != 1 || seen[0] != [2]bool{false, false} {
		t.Fatalf("fresh tests: no bandwidth, got %v", seen)
	}
	time.Sleep(5 * time.Millisecond)
	seen = nil
	step(db, st, true, measure, bw, notify)
	if len(seen) != 2 || seen[0] != [2]bool{true, false} || seen[1] != [2]bool{false, true} {
		t.Fatalf("Test Now reports testing then the result, got %v", seen)
	}
	var manual int
	_ = db.QueryRow("SELECT COUNT(*) FROM bandwidth WHERE trigger = 2").Scan(&manual)
	if manual != 1 {
		t.Fatal("manual test stored", manual)
	}
}
