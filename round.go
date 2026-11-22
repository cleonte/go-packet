package main

// One measurement round: every layer, measured in parallel, in about three seconds.

import (
	"net"
	"sync"
	"time"
)

const (
	pings       = 5
	pingGap     = 200 * time.Millisecond
	pingTimeout = time.Second
)

type Layer struct{ Ms, Loss, Jitter *float64 }

func layerFromPing(p PingStats) Layer { return Layer{p.Median(), p.Loss(), p.Jitter()} }

func (l Layer) JSON() map[string]any {
	var loss any
	if l.Loss != nil {
		loss = float64(int64(*l.Loss*1000+0.5)) / 1000
	}
	return map[string]any{"ms": round1(l.Ms), "loss": loss, "jitter_ms": round1(l.Jitter)}
}

type Round struct {
	AtMs int64
	Link LinkKind
	WiFi *WiFi
	// Gateway.Ms is nil when the router does not answer pings (many don't); that alone is no fault.
	Gateway      Layer
	GatewayKnown bool
	// Edge.Loss is nil when ICMP is blocked and Ms came from a TCP handshake.
	Edge      Layer
	Colo      string
	DnsMs     *float64
	Regions   [8]*float64
	Expensive bool
}

func (r *Round) FarMedian() *float64 {
	var v []float64
	for _, x := range r.Regions {
		if x != nil {
			v = append(v, *x)
		}
	}
	return median(v)
}

func (r *Round) RegionsUp() (n int) {
	for _, x := range r.Regions {
		if x != nil {
			n++
		}
	}
	return
}

func (r *Round) JSON(v Verdict) map[string]any {
	regions := make([]map[string]any, 8)
	for i, g := range Regions {
		regions[i] = map[string]any{"code": g.Code, "name": g.Name, "continent": g.Continent, "ms": round1(r.Regions[i])}
	}
	link := map[string]any{"kind": r.Link.Slug(), "label": r.Link.Label(), "wifi": nil}
	if w := r.WiFi; w != nil {
		link["wifi"] = map[string]any{"rssi_dbm": w.Rssi, "noise_dbm": w.Noise, "snr_db": w.SNR(),
			"tx_mbps": w.TxMbps, "channel": w.Channel, "band": w.BandLabel()}
	}
	var colo any
	if r.Colo != "" {
		colo = r.Colo
	}
	return map[string]any{
		"at": isoLocal(r.AtMs), "at_ms": r.AtMs, "verdict": v.JSON(), "link": link,
		"gateway": r.Gateway.JSON(),
		"edge":    map[string]any{"colo": colo, "ms": round1(r.Edge.Ms), "loss": r.Edge.Loss, "jitter_ms": round1(r.Edge.Jitter)},
		"dns":     map[string]any{"ms": round1(r.DnsMs)},
		"regions": regions, "path_expensive": r.Expensive,
	}
}

// runRound measures every layer in parallel.
func runRound(atMs int64, seq uint64) *Round {
	link := currentLink()
	exp, con := pathState()
	r := &Round{AtMs: atMs, Link: link.Kind, WiFi: link.WiFi, Expensive: exp || con}
	if link.Kind == LinkNone {
		return r
	}
	var wg sync.WaitGroup
	var gw, edge PingStats
	run := func(f func()) { wg.Add(1); go func() { defer wg.Done(); f() }() }
	if link.Gateway != nil {
		r.GatewayKnown = true
		run(func() { gw = ping(link.Gateway, pings, pingGap, pingTimeout) })
	}
	run(func() { edge = ping(edgeIP, pings, pingGap, pingTimeout) })
	run(func() { r.Colo = edgeColo() })
	run(func() { r.DnsMs = dnsMs(seq) })
	for i := range Regions {
		run(func() { r.Regions[i] = connectMs(Regions[i].Host, 2, 2*time.Second) })
	}
	wg.Wait()
	if link.Gateway != nil {
		r.Gateway = layerFromPing(gw)
	}
	if edge.Answered() {
		r.Edge = layerFromPing(edge)
	} else {
		// ICMP may be filtered; a TCP handshake still shows whether the edge is reachable.
		// Reachable over TCP means ICMP is filtered and loss is unknown; unreachable is total loss.
		ms := connectAddrMs(net.IP(edgeIP), 443, 2, 2*time.Second)
		r.Edge = Layer{Ms: ms}
		if ms == nil {
			r.Edge.Loss = fp(1)
		}
	}
	return r
}
