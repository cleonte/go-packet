package probe

import (
	"net"
	"testing"
	"time"

	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
)

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

func TestPingLoopback(t *testing.T) {
	st := ping(net.IPv4(127, 0, 0, 1), 3, 20*time.Millisecond, time.Second)
	if st.Sent == 0 {
		t.Skip("no unprivileged ICMP socket here")
	}
	if len(st.Rtts) == 0 {
		// macOS does not deliver loopback echo replies to unprivileged ICMP sockets, and CI
		// runners may block ICMP. Request/reply matching is covered by TestICMPRoundTrip.
		t.Skip("no loopback ICMP replies on this host")
	}
	if len(st.Rtts) != 3 {
		t.Fatalf("loopback must answer every ping, got %d of %d", len(st.Rtts), st.Sent)
	}
	if l := st.Loss(); l == nil || *l != 0 {
		t.Fatalf("loss = %v", l)
	}
	for _, r := range st.Rtts {
		if r <= 0 || r > 500 {
			t.Fatalf("implausible rtt %v", r)
		}
	}
	if ping(net.ParseIP("::1"), 1, 0, time.Second).Sent != 0 {
		t.Fatal("IPv6 targets are not measured")
	}
}

func TestConnectAddrMs(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback listener")
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	if ms := connectAddrMs(net.IPv4(127, 0, 0, 1), port, 2, time.Second); ms == nil || *ms <= 0 {
		t.Fatalf("handshake to a live listener: %v", ms)
	}
	ln.Close()
	if ms := connectAddrMs(net.IPv4(127, 0, 0, 1), port, 1, 200*time.Millisecond); ms != nil {
		t.Fatalf("a closed port must read as unreachable, got %v", *ms)
	}
}

// fakeDNS answers UDP queries on a local port. reply decides what to send back for a query.
func fakeDNS(t *testing.T, reply func(q []byte) [][]byte) string {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback UDP")
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			for _, r := range reply(append([]byte(nil), buf[:n]...)) {
				_, _ = pc.WriteTo(r, from)
			}
		}
	}()
	return pc.LocalAddr().String()
}

func answer(q []byte, rcode byte) []byte {
	r := append([]byte(nil), q...)
	r[2], r[3] = 0x81, 0x80|rcode // response, recursion available, rcode
	return r
}

func TestDNSViaFakeResolver(t *testing.T) {
	ok := fakeDNS(t, func(q []byte) [][]byte { return [][]byte{answer(q, 0)} })
	if ms := dnsVia(ok, "github.com", time.Second); ms == nil || *ms < 0 || *ms > 500 {
		t.Fatalf("a resolver that answers: %v", ms)
	}
	refused := fakeDNS(t, func(q []byte) [][]byte { return [][]byte{answer(q, 5)} })
	if ms := dnsVia(refused, "github.com", time.Second); ms != nil {
		t.Fatalf("REFUSED is a failure, got %v", *ms)
	}
	// An answer with someone else's ID is ignored; the real one right after it counts.
	noisy := fakeDNS(t, func(q []byte) [][]byte {
		stray := answer(q, 0)
		stray[0] ^= 0xff
		return [][]byte{stray, answer(q, 0)}
	})
	if ms := dnsVia(noisy, "github.com", time.Second); ms == nil {
		t.Fatal("a stray packet must not hide the real answer")
	}
	// Only a wrong-ID answer: never accepted.
	spoof := fakeDNS(t, func(q []byte) [][]byte {
		stray := answer(q, 0)
		stray[1] ^= 0xff
		return [][]byte{stray}
	})
	if ms := dnsVia(spoof, "github.com", 300*time.Millisecond); ms != nil {
		t.Fatalf("an answer with the wrong ID must not count, got %v", *ms)
	}
	silent := fakeDNS(t, func([]byte) [][]byte { return nil })
	start := time.Now()
	if ms := dnsVia(silent, "github.com", 300*time.Millisecond); ms != nil || time.Since(start) > 2*time.Second {
		t.Fatalf("a silent resolver times out (%v after %v)", ms, time.Since(start))
	}
}

func withURLs(t *testing.T, srv *httptest.Server) {
	t.Helper()
	oldT, oldD, oldU := traceURL, downURL, upURL
	traceURL, downURL, upURL = srv.URL+"/trace", srv.URL+"/__down?bytes=", srv.URL+"/__up"
	t.Cleanup(func() { traceURL, downURL, upURL = oldT, oldD, oldU })
}

func TestEdgeColoFromTracePage(t *testing.T) {
	page := "fl=1\nip=1.2.3.4\ncolo=SJC\nhttp=http/2\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, page) }))
	defer srv.Close()
	withURLs(t, srv)
	if got := edgeColo(); got != "SJC" {
		t.Fatalf("got %q", got)
	}
	page = "colo=<script>alert(1)</script>\n"
	if got := edgeColo(); got != "" {
		t.Fatalf("a hostile value must be dropped, got %q", got)
	}
	// A huge body is read only up to the cap: the colo line is past it, so it is not found.
	page = strings.Repeat("x", 10_000) + "\ncolo=LAX\n"
	if got := edgeColo(); got != "" {
		t.Fatalf("the body cap must hold, got %q", got)
	}
}

func fakeSpeedServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var received atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/__down", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("bytes"))
		_, _ = w.Write(make([]byte, n))
	})
	mux.HandleFunc("/__up", func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		received.Add(n)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &received
}

func TestRunStreamsMeasuresBytesAndRate(t *testing.T) {
	srv, received := fakeSpeedServer(t)
	withURLs(t, srv)
	c := Caps{Streams: 3, DownBytes: 200_000, UpBytes: 100_000, MaxSecs: 5}
	down, total := runStreams(c, false)
	if len(down) != 3 || total != 600_000 {
		t.Fatalf("download: %d streams, %d bytes", len(down), total)
	}
	if m := aggregateMbps(down); m == nil || *m <= 0 {
		t.Fatalf("rate: %v", m)
	}
	up, sent := runStreams(c, true)
	if len(up) != 3 || sent != 300_000 || received.Load() != 300_000 {
		t.Fatalf("upload: %d streams, counted %d, server got %d", len(up), sent, received.Load())
	}
}

func TestMeasureBandwidthQuickCheck(t *testing.T) {
	srv, _ := fakeSpeedServer(t)
	withURLs(t, srv)
	b := MeasureBandwidth(TrigScheduled, 123)
	if !b.Measured() || b.AtMs != 123 || b.Trigger != TrigScheduled || b.DownBytes != 4*2_500_000 || b.UpBytes != 4*1_000_000 {
		t.Fatalf("%+v", b)
	}
	if b.Rpm != nil {
		t.Fatal("quick checks carry no responsiveness figure")
	}
}

func TestStreamsIgnoreErrorPages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()
	withURLs(t, srv)
	down, total := runStreams(Caps{Streams: 2, DownBytes: 1000, MaxSecs: 2}, false)
	if len(down) != 0 || total != 0 {
		t.Fatalf("an error page is not a measurement: %d streams, %d bytes", len(down), total)
	}
	if b := MeasureBandwidth(TrigScheduled, 1); b.Measured() {
		t.Fatalf("a failing server must read as not measured: %+v", b)
	}
}
