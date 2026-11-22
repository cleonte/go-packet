package sampler

import (
	"log/slog"
	"os"
	"time"

	"github.com/cleonte/go-packet/internal/probe"
)

// The daemon itself is cheap (about 0.1% of one core), but a quick bandwidth check moves roughly
// 14 MB over the Wi-Fi radio every 10 minutes, and that is what costs battery. So on battery, or in
// Low Power Mode, background bandwidth checks pause (Test Now still works), and in Low Power Mode
// rounds run every two minutes instead of every minute.
//
// PACKET_POWER=always turns the policy off.

// policy is how much background work to do right now.
type policy struct {
	backgroundBandwidth bool          // scheduled and bad-round bandwidth checks may run
	roundEvery          time.Duration // time between latency rounds
}

func policyFor(p probe.Power, always bool) policy {
	pol := policy{backgroundBandwidth: true, roundEvery: time.Duration(roundEveryMs) * time.Millisecond}
	if always {
		return pol
	}
	if p.OnBattery || p.LowPower {
		pol.backgroundBandwidth = false
	}
	if p.LowPower {
		pol.roundEvery *= 2
	}
	return pol
}

// powerState is a seam for tests.
var powerState = probe.PowerState

var lastPolicy *policy

func currentPolicy() policy {
	p := powerState()
	pol := policyFor(p, os.Getenv("PACKET_POWER") == "always")
	if lastPolicy == nil {
		slog.Info("power state", "on_battery", p.OnBattery, "low_power_mode", p.LowPower,
			"background_bandwidth", pol.backgroundBandwidth, "round_every", pol.roundEvery)
	}
	if lastPolicy == nil || *lastPolicy != pol {
		if !pol.backgroundBandwidth || pol.roundEvery != time.Duration(roundEveryMs)*time.Millisecond {
			slog.Info("saving power: background bandwidth checks paused", "round_every", pol.roundEvery)
		} else if lastPolicy != nil {
			slog.Info("full speed again: background bandwidth checks resumed")
		}
		cp := pol
		lastPolicy = &cp
	}
	return pol
}
