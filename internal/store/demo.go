package store

import (
	"database/sql"
	"math/rand"

	"github.com/cleonte/go-packet/internal/num"
	"github.com/cleonte/go-packet/internal/probe"
	"github.com/cleonte/go-packet/internal/verdict"
)

// WriteDemo fills db with a synthetic 24 hours of rounds ending at endMs: mostly healthy, with a
// DNS stall, a stretch on weak Wi-Fi, an hour of provider trouble and a few hours on Ethernet. The
// data is made up (seeded, so it is the same every time); it is for screenshots and UI work and
// has nothing to do with any real network.
func WriteDemo(db *sql.DB, endMs int64) error {
	const minutes = 24 * 60
	rng := rand.New(rand.NewSource(1))
	around := func(mid, spread float64) float64 { return mid + (rng.Float64()*2-1)*spread }
	between := func(i, from, to int) bool { return i >= from && i < to } // i counts minutes ago
	baseRegions := [8]float64{190, 130, 55, 45, 270, 180, 250, 300}

	var prevRaw *verdict.Verdict
	for ago := minutes - 1; ago >= 0; ago-- {
		r := &probe.Round{AtMs: endMs - int64(ago)*60_000, GatewayKnown: true, Colo: "OTP"}
		ethernet := between(ago, 300, 420)
		weakWifi := between(ago, 900, 960)
		provider := between(ago, 600, 660)
		dnsStall := between(ago, 1100, 1110)

		switch {
		case ethernet:
			r.Link = probe.LinkEthernet
			r.Gateway = probe.Layer{Ms: num.Ptr(around(0.6, 0.2)), Loss: num.Ptr(0), Jitter: num.Ptr(0.1)}
		case weakWifi:
			r.Link = probe.LinkWiFi
			r.WiFi = &probe.WiFi{Rssi: int64(around(-81, 3)), Noise: -94, TxMbps: 72, Channel: 36, Band: 2}
			r.Gateway = probe.Layer{Ms: num.Ptr(around(75, 40)), Loss: num.Ptr(0.2), Jitter: num.Ptr(around(30, 15))}
		default:
			r.Link = probe.LinkWiFi
			r.WiFi = &probe.WiFi{Rssi: int64(around(-54, 3)), Noise: -94, TxMbps: 400, Channel: 36, Band: 2}
			r.Gateway = probe.Layer{Ms: num.Ptr(around(4.5, 1.5)), Loss: num.Ptr(0), Jitter: num.Ptr(around(0.8, 0.4))}
		}

		switch {
		case provider:
			r.Edge = probe.Layer{Ms: num.Ptr(around(190, 50)), Loss: num.Ptr(0.2 + 0.2*float64(rng.Intn(2))), Jitter: num.Ptr(around(40, 15))}
		case weakWifi:
			r.Edge = probe.Layer{Ms: num.Ptr(around(60, 25)), Loss: num.Ptr(0.2), Jitter: num.Ptr(around(30, 10))}
		default:
			r.Edge = probe.Layer{Ms: num.Ptr(around(16, 2)), Loss: num.Ptr(0), Jitter: num.Ptr(around(0.7, 0.3))}
		}

		if dnsStall {
			r.DnsMs = num.Ptr(around(260, 40))
		} else {
			r.DnsMs = num.Ptr(around(8, 2))
		}

		for i, base := range baseRegions {
			if provider && i%2 == 0 {
				continue // some regions unreachable during the provider's bad hour
			}
			x := around(base, 8)
			if provider {
				x *= 1.8
			}
			r.Regions[i] = num.Ptr(x)
		}

		raw := verdict.Judge(r, verdict.Baseline{}, nil)
		v := verdict.Settle(raw, prevRaw)
		prevRaw = &raw
		if err := InsertRound(db, r, v); err != nil {
			return err
		}

		// A quick check every 10 minutes, a full test every hour.
		at := r.AtMs
		if ago%10 == 0 {
			down := around(72, 6)
			if provider {
				down = around(9, 3)
			}
			b := probe.Bandwidth{AtMs: at + 1, Trigger: probe.TrigScheduled, DownMbps: num.Ptr(down), UpMbps: num.Ptr(down * 0.9), DownBytes: 10_000_000, UpBytes: 4_000_000}
			if err := InsertBandwidth(db, b); err != nil {
				return err
			}
		}
		if ago%60 == 30 {
			down, up := around(480, 25), around(95, 8)
			if provider {
				down, up = around(60, 15), around(20, 5)
			}
			b := probe.Bandwidth{AtMs: at + 2, Trigger: probe.TrigHourly, DownMbps: num.Ptr(down), UpMbps: num.Ptr(up), DownBytes: 140_000_000, UpBytes: 40_000_000}
			if err := InsertBandwidth(db, b); err != nil {
				return err
			}
		}
	}
	return nil
}
