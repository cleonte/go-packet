package sampler

// When a bandwidth test runs. Test Now always runs; these decide the automatic ones.

import (
	"os"
	"path/filepath"

	"github.com/cleonte/go-packet/internal/probe"
	"github.com/cleonte/go-packet/internal/store"
	"github.com/cleonte/go-packet/internal/verdict"
)

const (
	roundEveryMs   = 60_000
	quickEveryMs   = 10 * 60_000
	fullEveryMs    = 60 * 60_000
	badRoundMinGap = 3 * 60_000 // a bad round pulls a quick check forward, but never more often than this
	hourlyMarker   = "hourly-test"
)

// The hourly full test moves about 3.4 GB a day, so it runs only after the person turns it on.
// The setting is a marker file in the data folder.
func HourlyEnabled() bool { return hourlyEnabledIn(store.DataDir()) }

func hourlyEnabledIn(dir string) bool {
	fi, err := os.Lstat(filepath.Join(dir, hourlyMarker))
	return err == nil && fi.Mode().IsRegular()
}

func SetHourly(on bool) error { return setHourlyIn(store.DataDir(), on) }

func setHourlyIn(dir string, on bool) error {
	path := filepath.Join(dir, hourlyMarker)
	if on {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		// Whatever sits at the marker's name is replaced, never written through, so a planted
		// link cannot aim the write at another file.
		if _, err := os.Lstat(path); err == nil {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.WriteString("on\n")
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return os.Remove(path)
	}
	return nil
}

// bandwidthDue picks the automatic test, if any. lastQuick is the last test of any kind (a full
// test counts as a check too); lastFull the last hourly or Test Now run. A bad round pulls a
// check forward only when the network has just turned bad (prev was Good): a network that stays
// weak is not tested every few minutes, which would load it exactly when it is struggling.
func bandwidthDue(now int64, lastQuick, lastFull *int64, level, prev verdict.Level, expensive, hourly bool) (probe.Trigger, bool) {
	if expensive || level == verdict.Offline {
		return 0, false
	}
	since := func(t *int64) int64 {
		if t == nil {
			return 1 << 62
		}
		return now - *t
	}
	switch {
	case hourly && since(lastFull) >= fullEveryMs:
		return probe.TrigHourly, true
	case since(lastQuick) >= quickEveryMs:
		return probe.TrigScheduled, true
	case level >= verdict.Fair && prev == verdict.Good && since(lastQuick) >= badRoundMinGap:
		return probe.TrigBadRound, true
	}
	return 0, false
}
