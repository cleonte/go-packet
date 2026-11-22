package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cleonte/go-packet/internal/probe/probetest"
	"github.com/cleonte/go-packet/internal/sampler"
	"github.com/cleonte/go-packet/internal/store"
	"github.com/cleonte/go-packet/internal/verdict"
)

func newServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("PACKET_DATA_DIR", t.TempDir()) // the hourly marker lives here
	db, err := store.OpenAt(filepath.Join(t.TempDir(), "s.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Server{db: db, testNow: make(chan struct{}, 1)}
}

// do sends a request with a loopback Host (as a real client has) unless host is given.
func do(t *testing.T, s *Server, method, target, host string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.Host = "127.0.0.1:47831"
	if host != "" {
		req.Host = host
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	return w
}

func body(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("not JSON: %v: %q", err, w.Body.String())
	}
	return m
}

func TestHealth(t *testing.T) {
	w := do(t, newServer(t), "GET", "/v1/health", "", nil)
	m := body(t, w)
	if w.Code != 200 || m["ok"] != true {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if v, _ := m["version"].(string); v == "" {
		t.Fatalf("health must report the version: %v", m)
	}
}

func TestRebindingHostsAreRefused(t *testing.T) {
	s := newServer(t)
	for _, host := range []string{"evil.example.com", "evil.example.com:47831", "127.0.0.1.evil.com", "192.168.1.5:47831", ""} {
		req := httptest.NewRequest("GET", "/v1/status", nil)
		req.Host = host
		w := httptest.NewRecorder()
		s.handler().ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("Host %q: got %d, want 403", host, w.Code)
		}
	}
	for _, host := range []string{"127.0.0.1:47831", "localhost:47831", "[::1]:47831", "localhost"} {
		if w := do(t, s, "GET", "/v1/health", host, nil); w.Code != 200 {
			t.Errorf("Host %q: got %d, want 200", host, w.Code)
		}
	}
}

func TestPostsNeedTheHeader(t *testing.T) {
	s := newServer(t)
	for _, target := range []string{"/v1/test", "/v1/hourly?on=1"} {
		if w := do(t, s, "POST", target, "", nil); w.Code != 403 {
			t.Errorf("%s without header: %d, want 403", target, w.Code)
		}
		if w := do(t, s, "POST", target, "", map[string]string{"X-Packet": "yes"}); w.Code != 403 {
			t.Errorf("%s with the wrong header value: %d, want 403", target, w.Code)
		}
	}
	if len(s.testNow) != 0 {
		t.Fatal("a refused request must not queue a test")
	}
}

func TestTestNowQueuesOnce(t *testing.T) {
	s := newServer(t)
	h := map[string]string{"X-Packet": "1"}
	w := do(t, s, "POST", "/v1/test", "", h)
	if w.Code != 202 || body(t, w)["queued"] != true || len(s.testNow) != 1 {
		t.Fatalf("first: %d %s", w.Code, w.Body)
	}
	w = do(t, s, "POST", "/v1/test", "", h)
	if w.Code != 202 || body(t, w)["queued"] != false || len(s.testNow) != 1 {
		t.Fatalf("second must not queue another: %d %s", w.Code, w.Body)
	}
}

func TestHourlyToggle(t *testing.T) {
	s := newServer(t)
	h := map[string]string{"X-Packet": "1"}
	if w := do(t, s, "POST", "/v1/hourly?on=maybe", "", h); w.Code != 400 {
		t.Fatalf("bad value: %d", w.Code)
	}
	if w := do(t, s, "POST", "/v1/hourly", "", h); w.Code != 400 {
		t.Fatalf("missing value: %d", w.Code)
	}
	if w := do(t, s, "POST", "/v1/hourly?on=1", "", h); w.Code != 200 || body(t, w)["hourly_test"] != true {
		t.Fatalf("on: %d %s", w.Code, w.Body)
	}
	if !sampler.HourlyEnabled() {
		t.Fatal("marker not written")
	}
	if w := do(t, s, "POST", "/v1/hourly?on=0", "", h); w.Code != 200 || body(t, w)["hourly_test"] != false {
		t.Fatalf("off: %d %s", w.Code, w.Body)
	}
	if sampler.HourlyEnabled() {
		t.Fatal("marker not removed")
	}
}

func TestStatusBeforeAndAfterFirstRound(t *testing.T) {
	s := newServer(t)
	if m := body(t, do(t, s, "GET", "/v1/status", "", nil)); m["ready"] != false {
		t.Fatalf("before the first round: %v", m)
	}
	s.notify(sampler.Snapshot{Round: probetest.Healthy(), Verdict: verdict.Verdict{Level: verdict.Good, Sentence: "Network is healthy."}})
	m := body(t, do(t, s, "GET", "/v1/status", "", nil))
	if m["ready"] != true {
		t.Fatalf("after: %v", m)
	}
	if v, _ := m["verdict"].(map[string]any); v["level"] != "good" {
		t.Fatalf("verdict: %v", m["verdict"])
	}
	if rows, _ := m["rows"].([]any); len(rows) != 4 {
		t.Fatalf("want 4 layer rows, got %v", m["rows"])
	}
}

func TestHistoryRange(t *testing.T) {
	s := newServer(t)
	if w := do(t, s, "GET", "/v1/history?range=5y", "", nil); w.Code != 400 {
		t.Fatalf("bad range: %d", w.Code)
	}
	for _, r := range []string{"", "1h", "24h", "7d", "30d"} {
		target := "/v1/history"
		if r != "" {
			target += "?range=" + r
		}
		if w := do(t, s, "GET", target, "", nil); w.Code != 200 {
			t.Errorf("range %q: %d %s", r, w.Code, w.Body)
		}
	}
}

func TestHistoryPageAndRouting(t *testing.T) {
	s := newServer(t)
	w := do(t, s, "GET", "/", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Packet history") {
		t.Fatalf("page: %d", w.Code)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "connect-src 'self'") {
		t.Fatalf("CSP: %q", csp)
	}
	if w := do(t, s, "GET", "/nope", "", nil); w.Code != 404 {
		t.Errorf("unknown path: %d", w.Code)
	}
	if w := do(t, s, "GET", "/v1/test", "", nil); w.Code != 405 {
		t.Errorf("GET on a POST route: %d", w.Code)
	}
	if w := do(t, s, "DELETE", "/v1/status", "", nil); w.Code != 405 {
		t.Errorf("DELETE: %d", w.Code)
	}
}
