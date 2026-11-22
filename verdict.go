package main

// The verdict: which layer, if any, is making the connection bad. Layers are checked from the
// Mac outward, and when two layers are equally bad the one closer to the Mac is named, because
// a fault near the Mac shows up in everything past it.

import (
	"fmt"
	"strings"
)

type Level int

const (
	Good Level = iota
	Fair
	Poor
	Offline
)

func (l Level) Slug() string { return [...]string{"good", "fair", "poor", "offline"}[l] }
func (l Level) Mark() string { return [...]string{"🟢", "🟡", "🔴", "⚫"}[l] }

func levelFrom(v int64) Level {
	if v < 0 || v > 3 {
		return Good
	}
	return Level(v)
}

type Where int

const (
	Nowhere Where = iota
	WhereWiFi
	WhereLocal
	WhereIsp
	WhereDns
	WhereFar
)

func (w Where) Slug() string {
	return [...]string{"none", "wifi", "local", "isp", "dns", "far"}[w]
}
func (w Where) Label() string {
	return [...]string{"Nothing", "Wi-Fi", "Local network", "Internet provider", "DNS", "Far internet"}[w]
}

func whereFrom(v int64) Where {
	if v < 0 || v > 5 {
		return Nowhere
	}
	return Where(v)
}

// Baseline holds typical values learned from this Mac's own good rounds; nil until enough history.
type Baseline struct {
	EdgeMs, DnsMs *float64
	Regions       [8]*float64
	DownMbps      *float64
}

type Verdict struct {
	Level    Level
	At       Where
	Sentence string
}

func (v Verdict) JSON() map[string]any {
	return map[string]any{"level": v.Level.Slug(), "layer": v.At.Slug(), "layer_label": v.At.Label(), "sentence": v.Sentence}
}

// storedVerdict is the verdict a stored round was given.
func storedVerdict(level, at int64) Verdict {
	l, w := levelFrom(level), whereFrom(at)
	s := "Network was healthy."
	if l != Good {
		s = fmt.Sprintf("%s was %s.", w.Label(), l.Slug())
	}
	return Verdict{l, w, s}
}

type Finding struct {
	Level  Level
	At     Where
	Detail string
}

// atLeast compares with room for floating-point error (1 - 4/5 is 0.1999…).
func atLeast(v, t float64) bool { return v >= t-1e-9 }

// settle: one bad round is often a single lost ping. The verdict changes to Fair or Poor only
// when the round before was also bad; going offline, and getting better, show at once.
func settle(raw Verdict, prev *Verdict) Verdict {
	held := (raw.Level == Fair || raw.Level == Poor) && (prev == nil || prev.Level == Good)
	if !held {
		return raw
	}
	return Verdict{Good, Nowhere, fmt.Sprintf("Network is healthy. One round just now looked off (%s); watching the next.", strings.ToLower(raw.At.Label()))}
}

func fmtMs(v float64) string {
	if v < 10 {
		return fmt.Sprintf("%.1f ms", v)
	}
	return fmt.Sprintf("%.0f ms", v)
}

func fmtPct(v float64) string { return fmt.Sprintf("%.0f%%", v*100) }

// localFinding: the router, plus Wi-Fi signal when on Wi-Fi.
func localFinding(r *Round) Finding {
	wifi := r.Link == LinkWiFi
	at := WhereLocal
	if wifi {
		at = WhereWiFi
	}
	level := Good
	var why []string
	if w := r.WiFi; w != nil && wifi {
		// CoreWLAN reports noise 0 when it has no reading; then only the signal counts.
		hasSNR := w.Noise != 0
		weak := func(rssiCut, snrCut int64) bool { return w.Rssi <= rssiCut || (hasSNR && w.SNR() < snrCut) }
		say := func() string {
			if hasSNR && w.Rssi > -75 {
				return fmt.Sprintf("signal %d dBm but only %d dB above the noise", w.Rssi, w.SNR())
			}
			return fmt.Sprintf("signal %d dBm", w.Rssi)
		}
		if weak(-82, 10) {
			level = Poor
			why = append(why, say())
		} else if weak(-75, 20) {
			level = Fair
			why = append(why, say())
		}
	}
	// A VPN's "router" is the far end of the tunnel, so it gets Wi-Fi-like slack on latency.
	relaxed := wifi || r.Link == LinkOther
	fairMs, poorMs := 10.0, 50.0
	if relaxed {
		fairMs, poorMs = 40, 100
	}
	// Five pings a round: 0.2 is one lost, 0.4 two.
	const fairLoss, poorLoss = 0.2, 0.4
	// A router that answers no pings tells us nothing; only one that answers badly is a finding.
	if r.Gateway.Ms != nil {
		g := r.Gateway
		gms, loss, jitter := *g.Ms, val(g.Loss, 0), val(g.Jitter, 0)
		l := Good
		switch {
		case gms > poorMs || atLeast(loss, poorLoss):
			l = Poor
		case gms > fairMs || atLeast(loss, fairLoss) || jitter > fairMs:
			l = Fair
		}
		if l > Good {
			via := "router"
			if r.Link == LinkOther {
				via = "VPN gateway"
			}
			why = append(why, fmt.Sprintf("%s %s with %s loss", via, fmtMs(gms), fmtPct(loss)))
		}
		level = max(level, l)
	}
	return Finding{level, at, strings.Join(why, ", ")}
}

func ispFinding(r *Round, b Baseline, lastDown *float64) Finding {
	at := WhereIsp
	if r.Edge.Ms == nil {
		// Some networks block 1.1.1.1. If the far regions still answer, the line itself works.
		if r.RegionsUp()*2 >= len(r.Regions) {
			return Finding{Fair, at, "Cloudflare's edge is unreachable, though other regions answer"}
		}
		return Finding{Poor, at, "Cloudflare's edge is unreachable"}
	}
	e := *r.Edge.Ms
	base := val(b.EdgeMs, 20)
	loss, jitter := val(r.Edge.Loss, 0), val(r.Edge.Jitter, 0)
	level := Good
	switch {
	case atLeast(loss, 0.4) || e > max(4*base, 150):
		level = Poor
	case atLeast(loss, 0.2) || e > max(2*base+10, 60) || jitter > 30:
		level = Fair
	}
	detail := ""
	if level > Good {
		detail = fmt.Sprintf("edge %s with %s loss", fmtMs(e), fmtPct(loss))
	}
	if lastDown != nil && b.DownMbps != nil && *lastDown < 0.25**b.DownMbps {
		level = max(level, Fair)
		if detail != "" {
			detail += ", "
		}
		detail += fmt.Sprintf("download %.0f Mbps against a usual %.0f", *lastDown, *b.DownMbps)
	}
	return Finding{level, at, detail}
}

func dnsFinding(r *Round, b Baseline) Finding {
	at := WhereDns
	switch d := r.DnsMs; {
	case d == nil:
		return Finding{Poor, at, "lookups are failing"}
	case *d > 300:
		return Finding{Poor, at, "lookups take " + fmtMs(*d)}
	case *d > 100 && *d > 3*val(b.DnsMs, 10):
		return Finding{Fair, at, "lookups take " + fmtMs(*d)}
	}
	return Finding{Good, at, ""}
}

func farFinding(r *Round, b Baseline) Finding {
	at := WhereFar
	n := len(r.Regions)
	down := n - r.RegionsUp()
	slow := 0
	for i, now := range r.Regions {
		if now != nil && b.Regions[i] != nil && *now > *b.Regions[i]*1.5+40 {
			slow++
		}
	}
	switch {
	case down*2 >= n:
		return Finding{Poor, at, fmt.Sprintf("%d of %d regions unreachable", down, n)}
	case (down+slow)*2 >= n:
		return Finding{Fair, at, fmt.Sprintf("%d of %d regions slow or unreachable", down+slow, n)}
	}
	return Finding{Good, at, ""}
}

// findings: each layer from the Mac outward: local link, internet provider, DNS, far internet.
func findings(r *Round, b Baseline, lastDown *float64) [4]Finding {
	return [4]Finding{localFinding(r), ispFinding(r, b, lastDown), dnsFinding(r, b), farFinding(r, b)}
}

func judge(r *Round, b Baseline, lastDown *float64) Verdict {
	if r.Link == LinkNone {
		return Verdict{Offline, WhereLocal, "Offline: no network connection."}
	}
	if r.Edge.Ms == nil && r.RegionsUp() == 0 {
		switch {
		case r.Gateway.Ms != nil:
			return Verdict{Offline, WhereIsp, "Offline: your router answers, but nothing past it does."}
		case r.Link == LinkWiFi:
			return Verdict{Offline, WhereWiFi, "Offline: connected to Wi-Fi, but nothing answers."}
		}
		return Verdict{Offline, WhereLocal, "Offline: nothing answers past this Mac."}
	}
	fs := findings(r, b, lastDown)
	worst := Good
	for _, f := range fs {
		worst = max(worst, f.Level)
	}
	if worst == Good {
		return Verdict{Good, Nowhere, "Network is healthy. If something feels slow, it isn't the network."}
	}
	var f Finding
	for _, c := range fs {
		if c.Level == worst {
			f = c
			break
		}
	}
	finePast := ""
	switch {
	case (f.At == WhereWiFi || f.At == WhereLocal) && fs[1].Level == Good:
		finePast = " Past the router is fine."
	case f.At == WhereIsp && fs[0].Level == Good:
		finePast = " Your own network is fine."
	case (f.At == WhereDns || f.At == WhereFar) && fs[0].Level == Good && fs[1].Level == Good:
		finePast = " The connection itself is fine."
	}
	state := "is struggling"
	if worst == Poor {
		state = "is bad"
	}
	return Verdict{worst, f.At, fmt.Sprintf("%s %s: %s.%s", f.At.Label(), state, f.Detail, finePast)}
}
