// Package history buckets stored rounds for `packet history` and the history page. history for `packet history` and the daemon: rounds bucketed to a few hundred points.
package history

import (
	"database/sql"
	"math"

	"github.com/cleonte/menubar/packet-go/internal/num"
	"github.com/cleonte/menubar/packet-go/internal/probe"
	"github.com/cleonte/menubar/packet-go/internal/store"
	"github.com/cleonte/menubar/packet-go/internal/verdict"
)

var ranges = []struct {
	Name string
	Ms   int64
}{{"1h", 3_600_000}, {"24h", 86_400_000}, {"7d", 7 * 86_400_000}, {"30d", 30 * 86_400_000}}

const targetPoints = 360

func RangeMs(name string) (int64, bool) {
	for _, r := range ranges {
		if r.Name == name {
			return r.Ms, true
		}
	}
	return 0, false
}

func mean(v []float64) *float64 {
	if len(v) == 0 {
		return nil
	}
	var s float64
	for _, x := range v {
		s += x
	}
	m := s / float64(len(v))
	return &m
}

// predominant is the link most of a bucket's rounds were on; a tie goes to the later one.
func predominant(links []probe.LinkKind) probe.LinkKind {
	count := map[probe.LinkKind]int{}
	for _, l := range links {
		count[l]++
	}
	best := links[0]
	for _, l := range links {
		if count[l] >= count[best] {
			best = l
		}
	}
	return best
}

func bucketRounds(rounds []store.Stored, from, to int64) []map[string]any {
	width := max((to-from)/targetPoints, 60_000)
	var out []map[string]any
	i := 0
	for start := from; start < to && i <= len(rounds); start += width {
		end := start + width
		j := i
		for j < len(rounds) && rounds[j].Round.AtMs < end {
			j++
		}
		b := rounds[i:j]
		if len(b) > 0 {
			col := func(f func(store.Stored) *float64) []float64 {
				var v []float64
				for _, s := range b {
					if x := f(s); x != nil {
						v = append(v, *x)
					}
				}
				return v
			}
			loss, level, worst := 0.0, verdict.Good, b[0]
			var links []probe.LinkKind
			for _, s := range b {
				if s.Round.Edge.Loss != nil {
					loss = math.Max(loss, *s.Round.Edge.Loss)
				}
				if s.Round.Gateway.Ms != nil && s.Round.Gateway.Loss != nil {
					loss = math.Max(loss, *s.Round.Gateway.Loss)
				}
				if s.Verdict.Level > level {
					level = s.Verdict.Level
				}
				if s.Verdict.Level > worst.Verdict.Level {
					worst = s
				}
				links = append(links, s.Round.Link)
			}
			out = append(out, map[string]any{
				"t": start + width/2, "n": len(b),
				"gw":    num.Round1(mean(col(func(s store.Stored) *float64 { return s.Round.Gateway.Ms }))),
				"edge":  num.Round1(mean(col(func(s store.Stored) *float64 { return s.Round.Edge.Ms }))),
				"dns":   num.Round1(mean(col(func(s store.Stored) *float64 { return s.Round.DnsMs }))),
				"far":   num.Round1(mean(col(func(s store.Stored) *float64 { return s.Round.FarMedian() }))),
				"loss":  math.Round(loss*1000) / 1000,
				"level": level.Slug(), "layer": worst.Verdict.At.Slug(), "link": predominant(links).Slug(),
				"rssi": num.Round1(mean(col(func(s store.Stored) *float64 {
					if s.Round.WiFi == nil {
						return nil
					}
					return num.Ptr(float64(s.Round.WiFi.Rssi))
				}))),
			})
		}
		i = j
	}
	return out
}

// linkChanges lists every switch between wired and wireless. Drops to no connection or a VPN
// are not link changes; the verdict strip shows them.
func linkChanges(rounds []store.Stored) []map[string]any {
	var phys []store.Stored
	for _, s := range rounds {
		if s.Round.Link == probe.LinkEthernet || s.Round.Link == probe.LinkWiFi {
			phys = append(phys, s)
		}
	}
	out := []map[string]any{}
	for i := 1; i < len(phys); i++ {
		if phys[i-1].Round.Link != phys[i].Round.Link {
			out = append(out, map[string]any{"t": phys[i].Round.AtMs, "from": phys[i-1].Round.Link.Slug(), "to": phys[i].Round.Link.Slug()})
		}
	}
	return out
}

func Build(db *sql.DB, rng string, now int64) (map[string]any, error) {
	span, ok := RangeMs(rng)
	if !ok {
		span = 86_400_000
	}
	from := now - span
	rounds, err := store.RoundsBetween(db, from, now)
	if err != nil {
		return nil, err
	}
	// Only full tests carry the line's real speed.
	bws, err := store.BandwidthBetween(db, from, now)
	if err != nil {
		return nil, err
	}
	bw := []map[string]any{}
	for _, b := range bws {
		if b.Trigger.IsFull() {
			bw = append(bw, map[string]any{"t": b.AtMs, "down": num.Round1(b.DownMbps), "up": num.Round1(b.UpMbps), "rpm": num.Round1(b.Rpm), "trigger": b.Trigger.Slug()})
		}
	}
	bad := 0
	for _, s := range rounds {
		if s.Verdict.Level >= verdict.Fair {
			bad++
		}
	}
	buckets := bucketRounds(rounds, from, now)
	if buckets == nil {
		buckets = []map[string]any{}
	}
	return map[string]any{"range": rng, "from": from, "to": now, "rounds": len(rounds), "width": max(span/targetPoints, 60_000),
		"bad_rounds": bad, "buckets": buckets, "changes": linkChanges(rounds), "bandwidth": bw}, nil
}
