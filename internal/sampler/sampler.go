// Package sampler is the measuring loop: The measuring loop: a round every minute, bandwidth on its schedule, results to the store and
// to whoever is listening.
package sampler

import (
	"context"
	"database/sql"
	"time"

	"github.com/cleonte/go-packet/internal/clock"
	"github.com/cleonte/go-packet/internal/probe"
	"github.com/cleonte/go-packet/internal/store"
	"github.com/cleonte/go-packet/internal/verdict"
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
//
// retryBad is set for a round taken right after a wake from sleep or a network change, when the
// link is often not back yet: a round that looks bad is measured once more after retryDelay, and
// only the second result counts, so waking the Mac does not raise a false "offline".
func step(db *sql.DB, st *State, manual, retryBad bool,
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
	if retryBad && verdict.Judge(round, st.baseline, recent).Level != verdict.Good {
		time.Sleep(retryDelay)
		now = clock.NowMs()
		round = measure(now, st.seq)
		st.seq++
	}
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

const (
	// A gap this long between rounds means the Mac slept (or stalled): a round is 60 s apart.
	wakeGapMs = 2*roundEveryMs + 30_000
	// Path changes often come in bursts (Wi-Fi roaming, VPN up/down); wait for them to settle.
	pathDebounce = 3 * time.Second
	// But never measure because of the network more often than this.
	pathMinGapMs = 10_000
	// How long to wait for a default route to come back after a wake or change.
	linkWait = 30 * time.Second
	// How often the path generation is polled.
	pathPoll = 2 * time.Second
)

// retryDelay is how long to wait before re-measuring a round that looked bad right after a wake.
var retryDelay = 5 * time.Second

// IsWake reports whether the time since the last round shows the Mac was asleep. Wall-clock
// milliseconds are compared: Go's monotonic clock does not advance during sleep on macOS.
func IsWake(lastRoundMs, nowMs int64) bool {
	return lastRoundMs > 0 && nowMs-lastRoundMs > wakeGapMs
}

// watchPath signals kick whenever generation() changes, polling every poll.
func watchPath(ctx context.Context, generation func() int, poll time.Duration, kick chan<- struct{}) {
	last := generation()
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if g := generation(); g != last {
				last = g
				select {
				case kick <- struct{}{}:
				default: // one is already pending
				}
			}
		}
	}
}

// sleepCtx sleeps for d, and reports false if ctx ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// Run measures every minute until ctx ends. A signal on testNow runs Test Now. A wake from sleep
// or a change of network path runs a round early (after waiting for the link to return).
func Run(ctx context.Context, db *sql.DB, testNow <-chan struct{}, notify func(Snapshot)) {
	st := loadState(db, clock.NowMs())
	every := time.Duration(roundEveryMs) * time.Millisecond
	next := time.Now()
	var lastRound int64
	kick := make(chan struct{}, 1)
	go watchPath(ctx, probe.PathGeneration, pathPoll, kick)
	for {
		manual, afterChange := false, false
		select {
		case <-ctx.Done():
			return
		case <-testNow:
			manual = true
		case <-kick:
			if !sleepCtx(ctx, pathDebounce) {
				return
			}
			select { // changes during the debounce collapse into this one round
			case <-kick:
			default:
			}
			if clock.NowMs()-lastRound < pathMinGapMs {
				continue
			}
			afterChange = true
		case <-time.After(time.Until(next)):
			afterChange = IsWake(lastRound, clock.NowMs())
		}
		if afterChange {
			probe.WaitForLink(ctx, linkWait)
		}
		if !manual {
			next = time.Now().Add(every)
		}
		step(db, st, manual, afterChange, probe.RunRound, probe.MeasureBandwidth, notify)
		lastRound = clock.NowMs()
	}
}
