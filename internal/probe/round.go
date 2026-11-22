package probe

// One measurement round: every layer, measured in parallel, in about three seconds.

import (
	"net"
	"sync"
	"time"

	"github.com/cleonte/menubar/packet-go/internal/num"
)

const (
	pings       = 5
	pingGap     = 200 * time.Millisecond
	pingTimeout = time.Second
)

type Layer struct{ Ms, Loss, Jitter *float64 }

func layerFromPing(p PingStats) Layer { return Layer{p.Median(), p.Loss(), p.Jitter()} }

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
	return Median(v)
}

func (r *Round) RegionsUp() (n int) {
	for _, x := range r.Regions {
		if x != nil {
			n++
		}
	}
	return
}

// runRound measures every layer in parallel.
func RunRound(atMs int64, seq uint64) *Round {
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
			r.Edge.Loss = num.Ptr(1)
		}
	}
	return r
}
