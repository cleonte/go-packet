// Package probe measures the connection: link, ICMP, DNS, TCP and bandwidth.
//
// Measurements. Every host Packet talks to is named in this file.
package probe

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var edgeIP = net.IPv4(1, 1, 1, 1)

// The endpoints Packet talks to. Variables so tests can point them at a local server.
var (
	traceURL = "https://1.1.1.1/cdn-cgi/trace"
	downURL  = "https://speed.cloudflare.com/__down?bytes="
	upURL    = "https://speed.cloudflare.com/__up"
)

type Region struct{ Code, Name, Host, Continent string }

// Cloudflare is anycast and always answers nearby, so far latency comes from AWS regional
// endpoints, whose location is fixed. Order is the storage order (r0..r7).
var Regions = [8]Region{
	{"USW", "US West (N. California)", "dynamodb.us-west-1.amazonaws.com", "North America"},
	{"USE", "US East (Virginia)", "dynamodb.us-east-1.amazonaws.com", "North America"},
	{"LON", "London", "dynamodb.eu-west-2.amazonaws.com", "Europe"},
	{"FRA", "Frankfurt", "dynamodb.eu-central-1.amazonaws.com", "Europe"},
	{"TYO", "Tokyo", "dynamodb.ap-northeast-1.amazonaws.com", "Asia"},
	{"SIN", "Singapore", "dynamodb.ap-southeast-1.amazonaws.com", "Asia"},
	{"SAO", "São Paulo", "dynamodb.sa-east-1.amazonaws.com", "South America"},
	{"SYD", "Sydney", "dynamodb.ap-southeast-2.amazonaws.com", "Oceania"},
}

// Popular names for the DNS timing, rotated each round.
var dnsNames = [4]string{"www.apple.com", "www.google.com", "www.cloudflare.com", "github.com"}

// ---------- link ----------

type LinkKind int

const (
	LinkNone LinkKind = iota
	LinkEthernet
	LinkWiFi
	LinkOther
)

func (k LinkKind) Label() string {
	return [...]string{"No connection", "Ethernet", "Wi-Fi", "VPN or other"}[k]
}
func (k LinkKind) Slug() string { return [...]string{"none", "ethernet", "wifi", "other"}[k] }

type WiFi struct {
	Rssi, Noise int64
	TxMbps      float64
	Channel     int64
	Band        int64 // 1 = 2.4 GHz, 2 = 5 GHz, 3 = 6 GHz (CWChannelBand)
}

func (w WiFi) SNR() int64 { return w.Rssi - w.Noise }
func (w WiFi) BandLabel() string {
	switch w.Band {
	case 1:
		return "2.4 GHz"
	case 2:
		return "5 GHz"
	case 3:
		return "6 GHz"
	}
	return ""
}

type Link struct {
	Kind    LinkKind
	Gateway net.IP // nil when none
	WiFi    *WiFi
}

// parseRoute parses `route -n get default`.
func parseRoute(out string) (gw net.IP, iface string) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "gateway:"); ok {
			if ip := net.ParseIP(strings.TrimSpace(v)); ip != nil && ip.To4() != nil {
				gw = ip.To4()
			}
		} else if v, ok := strings.CutPrefix(line, "interface:"); ok {
			iface = strings.TrimSpace(v)
		}
	}
	return
}

func classify(iface, wifiIface string) LinkKind {
	switch {
	case iface == "":
		return LinkNone
	case iface == wifiIface:
		return LinkWiFi
	case strings.HasPrefix(iface, "en") || strings.HasPrefix(iface, "bridge"):
		return LinkEthernet
	}
	return LinkOther
}

// CurrentLink reads the default route and Wi-Fi state.
func CurrentLink() Link {
	out, _ := exec.Command("/sbin/route", "-n", "get", "default").Output()
	gw, iface := parseRoute(string(out))
	wifiIface, wifi := currentWiFi()
	kind := classify(iface, wifiIface)
	l := Link{Kind: kind, Gateway: gw}
	if kind == LinkWiFi {
		l.WiFi = wifi
	}
	return l
}

// ---------- ICMP ----------

type PingStats struct {
	Sent int
	Rtts []float64
}

func (p PingStats) Answered() bool { return len(p.Rtts) > 0 }
func (p PingStats) Loss() *float64 {
	if p.Sent == 0 {
		return nil
	}
	v := 1 - float64(len(p.Rtts))/float64(p.Sent)
	return &v
}
func (p PingStats) Median() *float64 { return Median(p.Rtts) }

// Jitter is the mean absolute difference between consecutive replies.
func (p PingStats) Jitter() *float64 {
	if len(p.Rtts) < 2 {
		return nil
	}
	var d float64
	for i := 1; i < len(p.Rtts); i++ {
		x := p.Rtts[i] - p.Rtts[i-1]
		if x < 0 {
			x = -x
		}
		d += x
	}
	v := d / float64(len(p.Rtts)-1)
	return &v
}

func Median(v []float64) *float64 {
	if len(v) == 0 {
		return nil
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	m := s[n/2]
	if n%2 == 0 {
		m = (s[n/2-1] + s[n/2]) / 2
	}
	return &m
}

func checksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i < len(b); i += 2 {
		if i+1 < len(b) {
			sum += uint32(binary.BigEndian.Uint16(b[i:]))
		} else {
			sum += uint32(b[i]) << 8
		}
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

func echoRequest(id, seq uint16, token [8]byte) []byte {
	p := []byte{8, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(p[4:], id)
	binary.BigEndian.PutUint16(p[6:], seq)
	p = append(p, token[:]...)
	binary.BigEndian.PutUint16(p[2:], checksum(p))
	return p
}

// isReply reports whether buf is the echo reply to seq carrying token. macOS hands datagram ICMP
// sockets the IP header too, so it is skipped when present.
func isReply(buf []byte, seq uint16, token [8]byte) bool {
	icmp := buf
	if len(buf) > 0 && buf[0]>>4 == 4 {
		ihl := int(buf[0]&0x0f) * 4
		if len(buf) < ihl {
			return false
		}
		icmp = buf[ihl:]
	}
	return len(icmp) >= 16 && icmp[0] == 0 && binary.BigEndian.Uint16(icmp[6:]) == seq && string(icmp[8:16]) == string(token[:])
}

var tokenMu sync.Mutex

// newToken is a per-call nonce, so pings started together cannot take each other's replies.
func newToken() (t [8]byte) {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	_, _ = rand.Read(t[:])
	return
}

// ping sends unprivileged ICMP echo (SOCK_DGRAM), one request at a time.
func ping(addr net.IP, count int, gap, timeout time.Duration) PingStats {
	var st PingStats
	ip4 := addr.To4()
	if ip4 == nil {
		return st
	}
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, syscall.IPPROTO_ICMP)
	if err != nil {
		return st
	}
	defer syscall.Close(fd)
	var to syscall.SockaddrInet4
	copy(to.Addr[:], ip4)
	tok := newToken()
	id := uint16(os.Getpid() & 0xffff)
	buf := make([]byte, 1500)
	for seq := 0; seq < count; seq++ {
		started := time.Now()
		st.Sent++
		if syscall.Sendto(fd, echoRequest(id, uint16(seq), tok), 0, &to) == nil {
			for {
				left := timeout - time.Since(started)
				if left <= 0 {
					break
				}
				tv := syscall.NsecToTimeval(left.Nanoseconds())
				_ = syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)
				n, from, err := syscall.Recvfrom(fd, buf, 0)
				if err != nil {
					break // timeout or error
				}
				if f, ok := from.(*syscall.SockaddrInet4); ok && f.Addr == to.Addr && isReply(buf[:n], uint16(seq), tok) {
					st.Rtts = append(st.Rtts, float64(time.Since(started).Microseconds())/1000)
					break
				}
			}
		}
		if seq+1 < count {
			time.Sleep(gap - time.Since(started))
		}
	}
	return st
}

// ---------- TCP, DNS, edge ----------

// resolveV4 is a system lookup with a deadline.
func resolveV4(host string, timeout time.Duration) net.IP {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil || len(ips) == 0 {
		return nil
	}
	return ips[0]
}

// connectMs is the TCP handshake time to host:443, best of tries. DNS time is excluded.
func connectMs(host string, tries int, timeout time.Duration) *float64 {
	ip := resolveV4(host, 2*time.Second)
	if ip == nil {
		return nil
	}
	return connectAddrMs(ip, 443, tries, timeout)
}

func connectAddrMs(ip net.IP, port, tries int, timeout time.Duration) *float64 {
	var best *float64
	addr := net.JoinHostPort(ip.String(), strconv.Itoa(port))
	for i := 0; i < tries; i++ {
		t := time.Now()
		c, err := net.DialTimeout("tcp4", addr, timeout)
		if err != nil {
			continue
		}
		c.Close()
		ms := float64(time.Since(t).Microseconds()) / 1000
		if best == nil || ms < *best {
			best = &ms
		}
	}
	return best
}

// parseResolver returns the first IPv4 nameserver in resolv.conf.
func parseResolver(conf string) net.IP {
	for _, l := range strings.Split(conf, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "nameserver"); ok {
			if ip := net.ParseIP(strings.TrimSpace(v)); ip != nil && ip.To4() != nil {
				return ip.To4()
			}
		}
	}
	return nil
}

func dnsQuery(id uint16, name string) []byte {
	q := []byte{byte(id >> 8), byte(id), 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, label := range strings.Split(name, ".") {
		q = append(q, byte(len(label)))
		q = append(q, label...)
	}
	return append(q, 0, 0, 1, 0, 1)
}

// dnsReply: ours means a standard-query response with our ID that repeats our question.
// Returns (answered, ours).
func dnsReply(buf, query []byte) (ok, ours bool) {
	ours = len(buf) >= len(query) && buf[0] == query[0] && buf[1] == query[1] &&
		buf[2]&0x80 != 0 && buf[2]&0x78 == 0 &&
		buf[4] == query[4] && buf[5] == query[5] && string(buf[12:len(query)]) == string(query[12:])
	return ours && buf[3]&0x0f == 0, ours
}

// dnsMs times the system resolver, queried directly so the local cache is skipped. With no IPv4
// resolver to ask (IPv6-only, encrypted DNS), it times a system lookup.
func dnsMs(round uint64) *float64 {
	name := dnsNames[round%uint64(len(dnsNames))]
	conf, _ := os.ReadFile("/etc/resolv.conf")
	server := parseResolver(string(conf))
	if server == nil {
		t := time.Now()
		if resolveV4(name, 2*time.Second) == nil {
			return nil
		}
		v := float64(time.Since(t).Microseconds()) / 1000
		return &v
	}
	return dnsVia(net.JoinHostPort(server.String(), "53"), name, 2*time.Second)
}

// dnsVia times one query sent to addr (host:port). It is connected, so only the resolver can
// answer, and the ID is random so nobody can guess it. nil when there is no valid answer in time.
func dnsVia(addr, name string, timeout time.Duration) *float64 {
	conn, err := net.Dial("udp4", addr) // connected: only the resolver can answer
	if err != nil {
		return nil
	}
	defer conn.Close()
	var idb [2]byte
	_, _ = rand.Read(idb[:])
	query := dnsQuery(binary.BigEndian.Uint16(idb[:]), name)
	deadline := time.Now().Add(timeout)
	_ = conn.SetDeadline(deadline)
	t := time.Now()
	if _, err := conn.Write(query); err != nil {
		return nil
	}
	buf := make([]byte, 1500)
	for time.Now().Before(deadline) {
		n, err := conn.Read(buf)
		if err != nil {
			return nil
		}
		if ok, ours := dnsReply(buf[:n], query); ours {
			if !ok {
				return nil
			}
			v := float64(time.Since(t).Microseconds()) / 1000
			return &v
		}
	}
	return nil
}

func parseColo(trace string) string {
	for _, l := range strings.Split(trace, "\n") {
		if v, ok := strings.CutPrefix(l, "colo="); ok {
			v = strings.TrimSpace(v)
			if len(v) >= 2 && len(v) <= 5 && strings.IndexFunc(v, func(r rune) bool {
				return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
			}) < 0 {
				return v
			}
		}
	}
	return ""
}

// edgeColo is the Cloudflare data center answering for this connection. The trace page is a few
// hundred bytes; reading at most 4 KB keeps a hostile endpoint from filling memory.
func edgeColo() string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", traceURL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return parseColo(string(body))
}

// ---------- bandwidth ----------

type Trigger int

const (
	TrigScheduled Trigger = iota
	TrigBadRound
	TrigManual
	TrigHourly
)

func (t Trigger) Slug() string {
	return [...]string{"scheduled", "bad-round", "manual", "hourly"}[t]
}

// IsFull: full tests read the line's real speed; quick checks are only compared with their own normal.
func (t Trigger) IsFull() bool { return t == TrigManual || t == TrigHourly }

type Caps struct {
	Streams            int
	DownBytes, UpBytes int64
	MaxSecs            int
}

// Quick checks stay small (about 14 MB). Full tests (hourly and Test Now) fill the line.
func capsFor(t Trigger) Caps {
	if t.IsFull() {
		return Caps{4, 25_000_000, 10_000_000, 8}
	}
	return Caps{4, 2_500_000, 1_000_000, 4}
}

type Bandwidth struct {
	AtMs      int64
	Trigger   Trigger
	DownMbps  *float64
	UpMbps    *float64
	DownBytes int64
	UpBytes   int64
	Rpm       *float64 // responsiveness under load, Test Now only
}

func (b Bandwidth) Measured() bool { return b.DownMbps != nil || b.UpMbps != nil }

type streamResult struct {
	bytes      int64
	start, end float64 // seconds since the test began
}

// aggregateMbps: all bytes over the span from the first stream moving data to the last finishing.
func aggregateMbps(streams []streamResult) *float64 {
	var bytes int64
	start, end := 1e18, 0.0
	for _, s := range streams {
		bytes += s.bytes
		start = min(start, s.start)
		end = max(end, s.end)
	}
	span := end - start
	if bytes <= 0 || span <= 0 {
		return nil
	}
	v := float64(bytes) * 8 / span / 1e6
	return &v
}

// countingReader feeds an upload body and counts what the transport actually read. The HTTP
// transport reads it on its own goroutine, so everything the stream goroutine reads back is atomic.
type countingReader struct {
	n       atomic.Int64 // bytes handed to the transport
	startNs atomic.Int64 // when the first byte was requested, in ns since t0 (0 = not yet)
	t0      time.Time
	left    int64 // only the transport's goroutine touches this
	chunk   []byte
}

func (r *countingReader) Read(p []byte) (int, error) {
	r.startNs.CompareAndSwap(0, max(time.Since(r.t0).Nanoseconds(), 1))
	if r.left <= 0 {
		return 0, io.EOF
	}
	k := min(int64(len(p)), int64(len(r.chunk)), r.left)
	copy(p, r.chunk[:k])
	r.left -= k
	r.n.Add(k)
	return int(k), nil
}

// noiseChunk is incompressible filler for uploads (xorshift).
func noiseChunk() []byte {
	t := newToken()
	x := uint64(0x9E3779B97F4A7C15) ^ uint64(t[0])
	b := make([]byte, 65536)
	for i := range b {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		b[i] = byte(x)
	}
	return b
}

func runStreams(c Caps, upload bool) (res []streamResult, total int64) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	t0 := time.Now()
	chunk := noiseChunk()
	for i := 0; i < c.Streams; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(c.MaxSecs)*time.Second)
			defer cancel()
			var n int64
			var start float64
			if upload {
				cr := &countingReader{left: c.UpBytes, chunk: chunk, t0: t0}
				req, _ := http.NewRequestWithContext(ctx, "POST", upURL, cr)
				req.Header.Set("Content-Type", "application/octet-stream")
				req.ContentLength = c.UpBytes
				resp, err := http.DefaultClient.Do(req)
				if resp != nil {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					if resp.StatusCode < 200 || resp.StatusCode > 299 {
						return // an error page is not a measurement
					}
				}
				if err != nil && ctx.Err() == nil {
					return
				}
				n = cr.n.Load()
				start = float64(cr.startNs.Load()) / 1e9
			} else {
				req, _ := http.NewRequestWithContext(ctx, "GET", downURL+strconv.FormatInt(c.DownBytes, 10), nil)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					return
				}
				defer resp.Body.Close()
				if resp.StatusCode < 200 || resp.StatusCode > 299 {
					return
				}
				start = time.Since(t0).Seconds()
				n, _ = io.Copy(io.Discard, resp.Body) // a deadline just ends the read; bytes so far count
			}
			mu.Lock()
			res = append(res, streamResult{n, start, time.Since(t0).Seconds()})
			total += n
			mu.Unlock()
		}()
	}
	wg.Wait()
	return
}

func MeasureBandwidth(t Trigger, nowMs int64) Bandwidth {
	if t == TrigManual {
		exp, con := pathState()
		if !(exp || con) {
			if b := networkQuality(nowMs); b != nil {
				return *b
			}
		}
	}
	c := capsFor(t)
	down, db := runStreams(c, false)
	up, ub := runStreams(c, true)
	return Bandwidth{AtMs: nowMs, Trigger: t, DownMbps: aggregateMbps(down), UpMbps: aggregateMbps(up), DownBytes: db, UpBytes: ub}
}

const (
	nqMaxSecs  = 12
	nqKillSecs = 30
)

func parseNetworkQuality(js string, atMs int64) *Bandwidth {
	var v map[string]any
	if json.Unmarshal([]byte(js), &v) != nil {
		return nil
	}
	num := func(k string) (float64, bool) { f, ok := v[k].(float64); return f, ok }
	dl, ok := num("dl_throughput")
	if !ok {
		return nil
	}
	mbps := func(bits float64) *float64 { x := bits / 1e6; return &x }
	b := &Bandwidth{AtMs: atMs, Trigger: TrigManual, DownMbps: mbps(dl)}
	if n, ok := num("dl_bytes_transferred"); ok {
		b.DownBytes = int64(max(n, 0))
	}
	if n, ok := num("ul_bytes_transferred"); ok {
		b.UpBytes = int64(max(n, 0))
	}
	if ul, ok := num("ul_throughput"); ok {
		b.UpMbps = mbps(ul)
	}
	if r, ok := num("responsiveness"); ok {
		b.Rpm = &r
	}
	return b
}

// networkQuality is macOS's own tool: it reads a multi-gig line's capacity plus responsiveness
// under load. Falls back to Cloudflare on any failure.
func networkQuality(nowMs int64) *Bandwidth {
	ctx, cancel := context.WithTimeout(context.Background(), nqKillSecs*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/networkQuality", "-c", "-M", strconv.Itoa(nqMaxSecs)).Output()
	if err != nil {
		return nil
	}
	return parseNetworkQuality(string(out), nowMs)
}

// rpmLabel puts responsiveness in words. The cut-offs are Packet's own, not Apple's.
func RpmLabel(rpm float64) string {
	switch {
	case rpm >= 800:
		return "High"
	case rpm >= 200:
		return "Medium"
	}
	return "Low"
}

// WaitForLink waits until there is a default route again (after a wake or a network change), up
// to timeout, and reports whether there is one. A short grace follows so DHCP and DNS can settle.
func WaitForLink(ctx context.Context, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if CurrentLink().Kind != LinkNone {
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
			return ctx.Err() == nil
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Second):
		}
	}
}
