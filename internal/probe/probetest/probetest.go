// Package probetest has fixtures shared by Packet's tests.
package probetest

import (
	"github.com/cleonte/go-packet/internal/num"
	"github.com/cleonte/go-packet/internal/probe"
)

// Healthy is a round with every layer fine, on Ethernet.
func Healthy() *probe.Round {
	return &probe.Round{
		Link: probe.LinkEthernet, Gateway: probe.Layer{Ms: num.Ptr(0.6), Loss: num.Ptr(0), Jitter: num.Ptr(0.1)}, GatewayKnown: true,
		Edge: probe.Layer{Ms: num.Ptr(12), Loss: num.Ptr(0), Jitter: num.Ptr(0.5)}, Colo: "SJC", DnsMs: num.Ptr(6),
		Regions: [8]*float64{num.Ptr(28), num.Ptr(84), num.Ptr(164), num.Ptr(170), num.Ptr(150), num.Ptr(180), num.Ptr(210), num.Ptr(231)},
	}
}
