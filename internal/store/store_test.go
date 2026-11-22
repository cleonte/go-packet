package store

import (
	"path/filepath"
	"testing"

	"github.com/cleonte/menubar/packet-go/internal/clock"
	"github.com/cleonte/menubar/packet-go/internal/probe"
	"github.com/cleonte/menubar/packet-go/internal/probe/probetest"
	"github.com/cleonte/menubar/packet-go/internal/verdict"
)

func TestStoreRoundTripAndBaseline(t *testing.T) {
	db, err := OpenAt(filepath.Join(t.TempDir(), "p.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	base := clock.NowMs() - 3_600_000
	for i := 0; i < 40; i++ {
		r := probetest.Healthy()
		r.AtMs = base + int64(i)*60_000
		r.Link, r.WiFi = probe.LinkWiFi, &probe.WiFi{Rssi: -60, Noise: -94, TxMbps: 800, Channel: 36, Band: 2}
		if err := InsertRound(db, r, verdict.Verdict{Level: verdict.Good, At: verdict.Nowhere}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := RoundsBetween(db, base, clock.NowMs())
	if err != nil || len(got) != 40 {
		t.Fatalf("%v %d", err, len(got))
	}
	r := got[0].Round
	if r.Link != probe.LinkWiFi || r.WiFi == nil || r.WiFi.Rssi != -60 || *r.Edge.Ms != 12 || r.Colo != "SJC" || *r.Regions[7] != 231 {
		t.Fatalf("round did not survive storage: %+v", r)
	}
	b := LoadBaseline(db, clock.NowMs())
	if b.EdgeMs == nil || *b.EdgeMs != 12 || *b.DnsMs != 6 || *b.Regions[0] != 28 {
		t.Fatalf("baseline %+v", b)
	}
	empty, _ := OpenAt(filepath.Join(t.TempDir(), "e.sqlite"))
	defer empty.Close()
	if LoadBaseline(empty, clock.NowMs()).EdgeMs != nil {
		t.Fatal("no baseline without history")
	}
}
