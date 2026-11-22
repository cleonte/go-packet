package probe

import (
	"net"
	"testing"
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
