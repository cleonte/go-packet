package verdict

import (
	"testing"

	"github.com/cleonte/go-packet/internal/num"
	"github.com/cleonte/go-packet/internal/probe"
	"github.com/cleonte/go-packet/internal/probe/probetest"
)

func TestHealthyIsGood(t *testing.T) {
	v := Judge(probetest.Healthy(), Baseline{}, nil)
	if v.Level != Good || v.At != Nowhere {
		t.Fatalf("got %+v", v)
	}
}

func TestWeakWiFiNamed(t *testing.T) {
	r := probetest.Healthy()
	r.Link, r.WiFi = probe.LinkWiFi, &probe.WiFi{Rssi: -77, Noise: -94}
	v := Judge(r, Baseline{}, nil)
	want := "Wi-Fi is struggling: signal -77 dBm. Past the router is fine."
	if v.Level != Fair || v.At != WhereWiFi || v.Sentence != want {
		t.Fatalf("got %+v", v)
	}
}

func TestSlowDNSNamed(t *testing.T) {
	r := probetest.Healthy()
	r.DnsMs = num.Ptr(225)
	v := Judge(r, Baseline{DnsMs: num.Ptr(10)}, nil)
	want := "DNS is struggling: lookups take 225 ms. The connection itself is fine."
	if v.Level != Fair || v.At != WhereDns || v.Sentence != want {
		t.Fatalf("got %+v", v)
	}
}

func TestOfflineCases(t *testing.T) {
	r := &probe.Round{Link: probe.LinkNone}
	if v := Judge(r, Baseline{}, nil); v.Level != Offline {
		t.Fatalf("no link: %+v", v)
	}
	r = probetest.Healthy()
	r.Edge, r.Regions = probe.Layer{Loss: num.Ptr(1)}, [8]*float64{}
	if v := Judge(r, Baseline{}, nil); v.Level != Offline || v.At != WhereIsp {
		t.Fatalf("router answers, nothing past: %+v", v)
	}
}

func TestSettleHoldsFirstBadRound(t *testing.T) {
	bad := Verdict{Fair, WhereDns, "x"}
	good := Verdict{Good, Nowhere, "ok"}
	if s := Settle(bad, nil); s.Level != Good {
		t.Fatal("first bad round must be held")
	}
	if s := Settle(bad, &good); s.Level != Good {
		t.Fatal("bad after good must be held")
	}
	if s := Settle(bad, &bad); s.Level != Fair {
		t.Fatal("two bad in a row must show")
	}
	off := Verdict{Offline, WhereLocal, "off"}
	if s := Settle(off, &good); s.Level != Offline {
		t.Fatal("offline must show at once")
	}
}
