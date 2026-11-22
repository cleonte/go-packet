package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func healthy() *Round {
	return &Round{
		Link: LinkEthernet, Gateway: Layer{fp(0.6), fp(0), fp(0.1)}, GatewayKnown: true,
		Edge: Layer{fp(12), fp(0), fp(0.5)}, Colo: "SJC", DnsMs: fp(6),
		Regions: [8]*float64{fp(28), fp(84), fp(164), fp(170), fp(150), fp(180), fp(210), fp(231)},
	}
}

func TestHealthyIsGood(t *testing.T) {
	v := judge(healthy(), Baseline{}, nil)
	if v.Level != Good || v.At != Nowhere {
		t.Fatalf("got %+v", v)
	}
}

func TestWeakWiFiNamed(t *testing.T) {
	r := healthy()
	r.Link, r.WiFi = LinkWiFi, &WiFi{Rssi: -77, Noise: -94}
	v := judge(r, Baseline{}, nil)
	want := "Wi-Fi is struggling: signal -77 dBm. Past the router is fine."
	if v.Level != Fair || v.At != WhereWiFi || v.Sentence != want {
		t.Fatalf("got %+v", v)
	}
}

func TestSlowDNSNamed(t *testing.T) {
	r := healthy()
	r.DnsMs = fp(225)
	v := judge(r, Baseline{DnsMs: fp(10)}, nil)
	want := "DNS is struggling: lookups take 225 ms. The connection itself is fine."
	if v.Level != Fair || v.At != WhereDns || v.Sentence != want {
		t.Fatalf("got %+v", v)
	}
}

func TestOfflineCases(t *testing.T) {
	r := &Round{Link: LinkNone}
	if v := judge(r, Baseline{}, nil); v.Level != Offline {
		t.Fatalf("no link: %+v", v)
	}
	r = healthy()
	r.Edge, r.Regions = Layer{Loss: fp(1)}, [8]*float64{}
	if v := judge(r, Baseline{}, nil); v.Level != Offline || v.At != WhereIsp {
		t.Fatalf("router answers, nothing past: %+v", v)
	}
}

func TestSettleHoldsFirstBadRound(t *testing.T) {
	bad := Verdict{Fair, WhereDns, "x"}
	good := Verdict{Good, Nowhere, "ok"}
	if s := settle(bad, nil); s.Level != Good {
		t.Fatal("first bad round must be held")
	}
	if s := settle(bad, &good); s.Level != Good {
		t.Fatal("bad after good must be held")
	}
	if s := settle(bad, &bad); s.Level != Fair {
		t.Fatal("two bad in a row must show")
	}
	off := Verdict{Offline, WhereLocal, "off"}
	if s := settle(off, &good); s.Level != Offline {
		t.Fatal("offline must show at once")
	}
}

func TestBandwidthSchedule(t *testing.T) {
	const m = 60_000
	now := int64(1000 * m)
	ago := func(min int64) *int64 { v := now - min*m; return &v }
	check := func(name string, got Trigger, ok bool, want Trigger, wantOK bool) {
		t.Helper()
		if ok != wantOK || (ok && got != want) {
			t.Errorf("%s: got (%v,%v) want (%v,%v)", name, got, ok, want, wantOK)
		}
	}
	full := ago(20)
	g, f := Good, Fair
	tr, ok := bandwidthDue(now, nil, nil, g, g, false, true)
	check("first round full", tr, ok, TrigHourly, true)
	tr, ok = bandwidthDue(now, ago(5), full, g, g, false, true)
	check("nothing due", tr, ok, 0, false)
	tr, ok = bandwidthDue(now, ago(10), full, g, g, false, true)
	check("quick due", tr, ok, TrigScheduled, true)
	tr, ok = bandwidthDue(now, ago(4), full, f, g, false, true)
	check("turned bad", tr, ok, TrigBadRound, true)
	tr, ok = bandwidthDue(now, ago(2), full, Poor, g, false, true)
	check("rate limited", tr, ok, 0, false)
	tr, ok = bandwidthDue(now, ago(4), full, f, f, false, true)
	check("staying bad", tr, ok, 0, false)
	tr, ok = bandwidthDue(now, nil, nil, g, g, false, false)
	check("hourly off", tr, ok, TrigScheduled, true)
	tr, ok = bandwidthDue(now, nil, nil, g, g, true, true)
	check("expensive path", tr, ok, 0, false)
	tr, ok = bandwidthDue(now, nil, nil, Offline, g, false, true)
	check("offline", tr, ok, 0, false)
}

func TestHourlyMarkerIgnoresLinks(t *testing.T) {
	dir := t.TempDir()
	if hourlyEnabledIn(dir) {
		t.Fatal("off by default")
	}
	target := filepath.Join(dir, "target")
	_ = os.WriteFile(target, []byte("keep"), 0o600)
	_ = os.Symlink(target, filepath.Join(dir, hourlyMarker))
	if hourlyEnabledIn(dir) {
		t.Fatal("a planted link must not turn it on")
	}
	if err := setHourlyIn(dir, true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Fatal("link target was written through")
	}
	if !hourlyEnabledIn(dir) {
		t.Fatal("on")
	}
	_ = setHourlyIn(dir, false)
	if hourlyEnabledIn(dir) {
		t.Fatal("off")
	}
}

func TestParseRouteAndClassify(t *testing.T) {
	gw, iface := parseRoute("   route to: default\ndestination: default\n    gateway: 192.168.1.1\n  interface: en0\n")
	if !gw.Equal(net.IPv4(192, 168, 1, 1)) || iface != "en0" {
		t.Fatalf("%v %v", gw, iface)
	}
	cases := []struct {
		iface, wifi string
		want        LinkKind
	}{{"en1", "en1", LinkWiFi}, {"en0", "en1", LinkEthernet}, {"utun4", "en1", LinkOther}, {"", "en1", LinkNone}}
	for _, c := range cases {
		if got := classify(c.iface, c.wifi); got != c.want {
			t.Errorf("classify(%q,%q)=%v", c.iface, c.wifi, got)
		}
	}
}

func TestPingStats(t *testing.T) {
	s := PingStats{Sent: 5, Rtts: []float64{2, 4, 3, 5}}
	if l := *s.Loss(); l < 0.1999 || l > 0.2001 {
		t.Fatal(l)
	}
	if *s.Median() != 3.5 {
		t.Fatal(*s.Median())
	}
	if j := *s.Jitter(); j < 1.666 || j > 1.667 {
		t.Fatal(j)
	}
}

func TestICMPRoundTrip(t *testing.T) {
	tok := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	req := echoRequest(7, 3, tok)
	reply := append([]byte(nil), req...)
	reply[0] = 0
	if !isReply(reply, 3, tok) || isReply(reply, 4, tok) || isReply(reply, 3, [8]byte{}) || isReply(req, 3, tok) {
		t.Fatal("reply matching")
	}
	withIP := append([]byte{0x45, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 1, 1, 1, 1, 10, 0, 0, 2}, reply...)
	if !isReply(withIP, 3, tok) {
		t.Fatal("must skip the IP header")
	}
}

func TestDNSReplyMatching(t *testing.T) {
	q := dnsQuery(0x1234, "github.com")
	ok := append([]byte(nil), q...)
	ok[2], ok[3] = 0x81, 0x80
	if good, ours := dnsReply(ok, q); !good || !ours {
		t.Fatal("answered")
	}
	refused := append([]byte(nil), ok...)
	refused[3] = 0x85
	if good, ours := dnsReply(refused, q); good || !ours {
		t.Fatal("refused is ours but not good")
	}
	other := append([]byte(nil), ok...)
	other[0] = 0xff
	if _, ours := dnsReply(other, q); ours {
		t.Fatal("wrong id is not ours")
	}
}

func TestParseColoAndResolver(t *testing.T) {
	if parseColo("fl=1\ncolo=SJC\nhttp=2") != "SJC" || parseColo("colo=<script>") != "" || parseColo("colo=A") != "" {
		t.Fatal("colo")
	}
	if ip := parseResolver("search x\nnameserver ::1\nnameserver 192.168.1.1\n"); !ip.Equal(net.IPv4(192, 168, 1, 1)) {
		t.Fatal(ip)
	}
}

func TestParseNetworkQuality(t *testing.T) {
	js := `{"dl_throughput":81806872,"ul_throughput":50000000,"responsiveness":812.5,"dl_bytes_transferred":1000,"ul_bytes_transferred":-5,"il_h2_req_resp":[1,2],"start_date":"x"}`
	b := parseNetworkQuality(js, 42)
	if b == nil || *b.DownMbps < 81.8 || *b.DownMbps > 81.81 || *b.Rpm != 812.5 || b.UpBytes != 0 || b.Trigger != TrigManual {
		t.Fatalf("%+v", b)
	}
	if parseNetworkQuality(`{"x":1}`, 1) != nil || parseNetworkQuality("nope", 1) != nil {
		t.Fatal("bad input must be nil")
	}
}

func TestAggregateMbps(t *testing.T) {
	// 4 streams x 1 MB over 1 s = 32 Mbps
	s := []streamResult{{1_000_000, 0, 1}, {1_000_000, 0, 1}, {1_000_000, 0.1, 1}, {1_000_000, 0, 1}}
	if v := *aggregateMbps(s); v < 31.9 || v > 32.1 {
		t.Fatal(v)
	}
	if aggregateMbps(nil) != nil {
		t.Fatal("no streams")
	}
}

func TestParseWhen(t *testing.T) {
	now := time.Date(2026, 10, 12, 15, 0, 0, 0, time.Local).UnixMilli()
	at := func(y, mo, d, h, mi int) int64 {
		return time.Date(y, time.Month(mo), d, h, mi, 0, 0, time.Local).UnixMilli()
	}
	ok := []struct {
		in   string
		want int64
	}{
		{"30m", now - 1_800_000}, {"2h ago", now - 7_200_000}, {"14:30", now - 1_800_000},
		{"16:00", at(2026, 10, 11, 16, 0)}, {"2026-10-12 14:30", now - 1_800_000}, {"2026-10-12T14:30", now - 1_800_000},
	}
	for _, c := range ok {
		if got, good := parseWhen(c.in, now); !good || got != c.want {
			t.Errorf("parseWhen(%q)=%v,%v want %v", c.in, got, good, c.want)
		}
	}
	for _, bad := range []string{"25:00", "2026-02-30 14:30", "soon", "2026-10-12 1é:30"} {
		if _, good := parseWhen(bad, now); good {
			t.Errorf("parseWhen(%q) should fail", bad)
		}
	}
	first := at(2026, 11, 1, 9, 0)
	if got, _ := parseWhen("10:00", first); got != at(2026, 10, 31, 10, 0) {
		t.Error("yesterday across a month end")
	}
}

func TestStoreRoundTripAndBaseline(t *testing.T) {
	db, err := openStoreAt(filepath.Join(t.TempDir(), "p.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	base := nowMs() - 3_600_000
	for i := 0; i < 40; i++ {
		r := healthy()
		r.AtMs = base + int64(i)*60_000
		r.Link, r.WiFi = LinkWiFi, &WiFi{Rssi: -60, Noise: -94, TxMbps: 800, Channel: 36, Band: 2}
		if err := insertRound(db, r, Verdict{Good, Nowhere, ""}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := roundsBetween(db, base, nowMs())
	if err != nil || len(got) != 40 {
		t.Fatalf("%v %d", err, len(got))
	}
	r := got[0].Round
	if r.Link != LinkWiFi || r.WiFi == nil || r.WiFi.Rssi != -60 || *r.Edge.Ms != 12 || r.Colo != "SJC" || *r.Regions[7] != 231 {
		t.Fatalf("round did not survive storage: %+v", r)
	}
	b := loadBaseline(db, nowMs())
	if b.EdgeMs == nil || *b.EdgeMs != 12 || *b.DnsMs != 6 || *b.Regions[0] != 28 {
		t.Fatalf("baseline %+v", b)
	}
	empty, _ := openStoreAt(filepath.Join(t.TempDir(), "e.sqlite"))
	defer empty.Close()
	if loadBaseline(empty, nowMs()).EdgeMs != nil {
		t.Fatal("no baseline without history")
	}
	h, err := buildHistory(db, "24h", nowMs())
	if err != nil || h["rounds"].(int) != 40 {
		t.Fatalf("%v %v", err, h["rounds"])
	}
}

func TestStepFlow(t *testing.T) {
	db, _ := openStoreAt(filepath.Join(t.TempDir(), "s.sqlite"))
	defer db.Close()
	st := loadState(db, nowMs())
	now := nowMs()
	st.lastQuick = &Bandwidth{AtMs: now, Trigger: TrigScheduled}
	st.lastFull = &Bandwidth{AtMs: now, Trigger: TrigHourly}
	measure := func(at int64, _ uint64) *Round { r := healthy(); r.AtMs = at; return r }
	bw := func(tr Trigger, at int64) Bandwidth {
		return Bandwidth{AtMs: at, Trigger: tr, DownMbps: fp(300), UpMbps: fp(200), DownBytes: 1, UpBytes: 1}
	}
	var seen [][2]bool
	notify := func(s Snapshot) { seen = append(seen, [2]bool{s.Testing, s.ManualDone}) }
	step(db, st, false, measure, bw, notify)
	if len(seen) != 1 || seen[0] != [2]bool{false, false} {
		t.Fatalf("fresh tests: no bandwidth, got %v", seen)
	}
	time.Sleep(5 * time.Millisecond)
	seen = nil
	step(db, st, true, measure, bw, notify)
	if len(seen) != 2 || seen[0] != [2]bool{true, false} || seen[1] != [2]bool{false, true} {
		t.Fatalf("Test Now reports testing then the result, got %v", seen)
	}
	var manual int
	_ = db.QueryRow("SELECT COUNT(*) FROM bandwidth WHERE trigger = 2").Scan(&manual)
	if manual != 1 {
		t.Fatal("manual test stored", manual)
	}
}

func TestShortRowsStayShort(t *testing.T) {
	r := healthy()
	r.Link, r.WiFi = LinkWiFi, &WiFi{Rssi: -52, Noise: -94, TxMbps: 400, Channel: 36, Band: 2}
	rows := []string{linkShort(r), edgeShort(r), dnsShort(r), worldShort(r), speedShort(&Bandwidth{AtMs: nowMs(), DownMbps: fp(77), UpMbps: fp(60), Rpm: fp(136)}, nowMs())}
	for _, s := range rows {
		if n := len([]rune(s)); n > 52 {
			t.Errorf("short row too long (%d): %q", n, s)
		}
	}
	if linkShort(r) != "Wi-Fi -52 dBm · 5 GHz · router 0.6 ms" {
		t.Errorf("got %q", linkShort(r))
	}
	r.Gateway.Loss = fp(0.2)
	if got := linkShort(r); got != "Wi-Fi -52 dBm · 5 GHz · router 0.6 ms · 20% loss" {
		t.Errorf("loss shown only when present: %q", got)
	}
}
