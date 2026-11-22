package report

import (
	"testing"

	"github.com/cleonte/go-packet/internal/clock"
	"github.com/cleonte/go-packet/internal/num"
	"github.com/cleonte/go-packet/internal/probe"
	"github.com/cleonte/go-packet/internal/probe/probetest"
)

func TestShortRowsStayShort(t *testing.T) {
	r := probetest.Healthy()
	r.Link, r.WiFi = probe.LinkWiFi, &probe.WiFi{Rssi: -52, Noise: -94, TxMbps: 400, Channel: 36, Band: 2}
	rows := []string{linkShort(r), edgeShort(r), dnsShort(r), worldShort(r), speedShort(&probe.Bandwidth{AtMs: clock.NowMs(), DownMbps: num.Ptr(77), UpMbps: num.Ptr(60), Rpm: num.Ptr(136)}, clock.NowMs())}
	for _, s := range rows {
		if n := len([]rune(s)); n > 52 {
			t.Errorf("short row too long (%d): %q", n, s)
		}
	}
	if linkShort(r) != "Wi-Fi -52 dBm · 5 GHz · router 0.6 ms" {
		t.Errorf("got %q", linkShort(r))
	}
	r.Gateway.Loss = num.Ptr(0.2)
	if got := linkShort(r); got != "Wi-Fi -52 dBm · 5 GHz · router 0.6 ms · 20% loss" {
		t.Errorf("loss shown only when present: %q", got)
	}
}
