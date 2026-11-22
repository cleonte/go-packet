// Package sampler is the measuring loop: The measuring loop: a round every minute, bandwidth on its schedule, results to the store and
// to whoever is listening.
package sampler

import (
	"context"
	"database/sql"
	"time"

	"github.com/cleonte/menubar/packet-go/internal/clock"
	"github.com/cleonte/menubar/packet-go/internal/probe"
	"github.com/cleonte/menubar/packet-go/internal/store"
	"github.com/cleonte/menubar/packet-go/internal/verdict"
)

const baselineRefreshMs = 30 * 60_000

type Snapshot struct {
	Round *probe.Round
	// Verdict is settled: a single bad round is not enough to change it (see settle).
	Verdict verdict.Verdict
	// Findings are each layer as this round measured it, before settling.
	Findings [4]verdict.Finding
	// Full is the last full test: the line's real speed.
	Full *probe.Bandwidth
	// Quick is the last quick check, with its share of the usual download (nil until there is a usual).
	Quick      *probe.Bandwidth
	QuickShare *float64
	Testing    bool // a bandwidth test is running right after this round
	ManualDone bool // this snapshot ends a Test Now
}

type State struct {
	baseline    verdict.Baseline
	baselineAt  int64
	lastQuick   *probe.Bandwidth
	lastFull    *probe.Bandwidth
	prevRaw     *verdict.Verdict
	prevSettled verdict.Level
	triedAny    *int64 // last test of any kind, measured or not: a failed test waits for its schedule
	triedFull   *int64
	seq         uint64
	prunedAt    int64
}

func loadState(db *sql.DB, now int64) *State {
	return &State{baseline: store.LoadBaseline(db, now), baselineAt: now, lastQuick: store.LatestQuick(db), lastFull: store.LatestFull(db)}
}

func share(quick *probe.Bandwidth, b verdict.Baseline) *float64 {
	if quick != nil && quick.DownMbps != nil && b.DownMbps != nil && *b.DownMbps > 0 {
		v := *quick.DownMbps / *b.DownMbps
		return &v
	}
	return nil
}

func maxTime(ts ...*int64) *int64 {
	var m *int64
	for _, t := range ts {
		if t != nil && (m == nil || *t > *m) {
			m = t
		}
	}
	return m
}

func atOf(b *probe.Bandwidth) *int64 {
	if b == nil {
		return nil
	}
	return &b.AtMs
}

// step runs one round and the bandwidth test it calls for. manual is Test Now.
func step(db *sql.DB, st *State, manual bool,
	measure func(int64, uint64) *probe.Round, bandwidth func(probe.Trigger, int64) probe.Bandwidth, notify func(Snapshot)) {
	now := clock.NowMs()
	if now-st.baselineAt > baselineRefreshMs {
		st.baseline, st.baselineAt = store.LoadBaseline(db, now), now
	}
	if now-st.prunedAt > 86_400_000 {
		store.Prune(db, now)
		st.prunedAt = now
	}
	round := measure(now, st.seq)
	st.seq++
	recent := store.RecentScheduledDown(db, now)
	fs := verdict.Findings(round, st.baseline, recent)
	raw := verdict.Judge(round, st.baseline, recent)
	prevLevel := st.prevSettled
	v := verdict.Settle(raw, st.prevRaw)
	st.prevRaw, st.prevSettled = &raw, v.Level
	if store.InsertRound(db, round, v) != nil {
		time.Sleep(250 * time.Millisecond)
		_ = store.InsertRound(db, round, v)
	}
	var due probe.Trigger
	var isDue bool
	if manual {
		due, isDue = probe.TrigManual, true
	} else {
		due, isDue = bandwidthDue(now, maxTime(atOf(st.lastQuick), atOf(st.lastFull), st.triedAny),
			maxTime(atOf(st.lastFull), st.triedFull), v.Level, prevLevel, round.Expensive, HourlyEnabled())
	}
	snap := Snapshot{Round: round, Verdict: v, Findings: fs, Full: st.lastFull, Quick: st.lastQuick,
		QuickShare: share(st.lastQuick, st.baseline), Testing: isDue}
	notify(snap)
	if !isDue {
		return
	}
	bw := bandwidth(due, clock.NowMs())
	st.triedAny = &bw.AtMs
	if due.IsFull() {
		st.triedFull = &bw.AtMs
	}
	_ = store.InsertBandwidth(db, bw)
	if bw.Measured() {
		if bw.Trigger.IsFull() {
			st.lastFull, snap.Full = &bw, &bw
		} else {
			st.lastQuick, snap.Quick = &bw, &bw
			snap.QuickShare = share(&bw, st.baseline)
		}
	}
	snap.Testing, snap.ManualDone = false, manual
	notify(snap)
}

// runSampler measures every minute until ctx ends. A signal on testNow runs Test Now.
func Run(ctx context.Context, db *sql.DB, testNow <-chan struct{}, notify func(Snapshot)) {
	st := loadState(db, clock.NowMs())
	every := time.Duration(roundEveryMs) * time.Millisecond
	next := time.Now()
	for {
		manual := false
		select {
		case <-ctx.Done():
			return
		case <-testNow:
			manual = true
		case <-time.After(time.Until(next)):
		}
		if !manual {
			next = next.Add(every)
			if next.Before(time.Now()) {
				next = time.Now().Add(every)
			}
		}
		step(db, st, manual, probe.RunRound, probe.MeasureBandwidth, notify)
	}
}
