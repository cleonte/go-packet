package store

import (
	"path/filepath"
	"testing"

	"github.com/cleonte/go-packet/internal/clock"
	"github.com/cleonte/go-packet/internal/probe"
	"github.com/cleonte/go-packet/internal/probe/probetest"
	"github.com/cleonte/go-packet/internal/verdict"
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

func TestWriteDemoTellsAStory(t *testing.T) {
	db, err := OpenAt(filepath.Join(t.TempDir(), "demo.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	end := clock.NowMs()
	if err := WriteDemo(db, end); err != nil {
		t.Fatal(err)
	}
	rounds, err := RoundsBetween(db, end-25*3_600_000, end+1)
	if err != nil || len(rounds) != 1440 {
		t.Fatalf("want 1440 rounds, got %d (%v)", len(rounds), err)
	}
	layers := map[verdict.Where]int{}
	links := map[probe.LinkKind]bool{}
	for _, s := range rounds {
		if s.Verdict.Level >= verdict.Fair {
			layers[s.Verdict.At]++
		}
		links[s.Round.Link] = true
	}
	for _, w := range []verdict.Where{verdict.WhereDns, verdict.WhereWiFi, verdict.WhereIsp} {
		if layers[w] == 0 {
			t.Errorf("the demo day should show trouble at %v, got %v", w, layers)
		}
	}
	if !links[probe.LinkEthernet] || !links[probe.LinkWiFi] {
		t.Error("the demo day should switch between Wi-Fi and Ethernet")
	}
	if full := LatestFull(db); full == nil {
		t.Error("the demo day should have full speed tests")
	}
	// Same seed, same data: screenshots are reproducible.
	db2, _ := OpenAt(filepath.Join(t.TempDir(), "demo2.sqlite"))
	defer db2.Close()
	if err := WriteDemo(db2, end); err != nil {
		t.Fatal(err)
	}
	rounds2, _ := RoundsBetween(db2, end-25*3_600_000, end+1)
	if *rounds[700].Round.Edge.Ms != *rounds2[700].Round.Edge.Ms {
		t.Error("the demo data must be deterministic")
	}
}
