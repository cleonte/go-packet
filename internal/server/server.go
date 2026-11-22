// Package server runs the daemon: the measuring loop plus a localhost-only API.
//
// The daemon's local API. It listens on 127.0.0.1 only; A menu bar app (or curl) reads it.
//
//	GET  /                     history page (open in a browser)
//	GET  /v1/status            latest round, verdict, layer rows, bandwidth
//	GET  /v1/history?range=24h bucketed history
//	POST /v1/hourly?on=1|0     turn the hourly line-speed test on or off (needs X-Packet: 1)
//	POST /v1/test              run Test Now (needs header X-Packet: 1)
//	GET  /v1/health            liveness and the running version
//
// Writes need a custom header, which a web page cannot send cross-origin without a preflight we
// never answer, and the Host header must be loopback, which defeats DNS rebinding.
package server

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/cleonte/go-packet/internal/clock"
	"github.com/cleonte/go-packet/internal/history"
	"github.com/cleonte/go-packet/internal/probe"
	"github.com/cleonte/go-packet/internal/report"
	"github.com/cleonte/go-packet/internal/sampler"
	"github.com/cleonte/go-packet/internal/store"
	"github.com/cleonte/go-packet/internal/version"
)

const defaultPort = "47831"

//go:embed web/history.html
var historyPage []byte

func ListenAddr() string {
	p := os.Getenv("PACKET_PORT")
	if p == "" {
		p = defaultPort
	}
	return "127.0.0.1:" + p
}

type Server struct {
	db      *sql.DB
	testNow chan struct{}
	mu      sync.RWMutex
	snap    *sampler.Snapshot
	snapAt  int64
}

func (s *Server) notify(sn sampler.Snapshot) {
	s.mu.Lock()
	s.snap, s.snapAt = &sn, clock.NowMs()
	s.mu.Unlock()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func loopbackHost(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	return host == "127.0.0.1" || host == "localhost" || host == "[::1]" || host == "::1"
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "version": version.String()})
	})
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		sn, at := s.snap, s.snapAt
		s.mu.RUnlock()
		if sn == nil {
			writeJSON(w, 200, map[string]any{"ready": false})
			return
		}
		out := report.SnapshotJSON(*sn, clock.NowMs())
		out["ready"] = true
		out["updated_ms"] = at
		out["hourly_test"] = sampler.HourlyEnabled()
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'")
		_, _ = w.Write(historyPage)
	})
	mux.HandleFunc("GET /v1/history", func(w http.ResponseWriter, r *http.Request) {
		rng := r.URL.Query().Get("range")
		if rng == "" {
			rng = "24h"
		}
		if _, ok := history.RangeMs(rng); !ok {
			writeJSON(w, 400, map[string]any{"error": "range must be 1h, 24h, 7d or 30d"})
			return
		}
		h, err := history.Build(s.db, rng, clock.NowMs())
		if err != nil {
			slog.Warn("history unavailable", "range", rng, "err", err)
			writeJSON(w, 500, map[string]any{"error": "history unavailable"})
			return
		}
		writeJSON(w, 200, h)
	})
	mux.HandleFunc("POST /v1/test", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Packet") != "1" {
			writeJSON(w, 403, map[string]any{"error": "missing X-Packet header"})
			return
		}
		select {
		case s.testNow <- struct{}{}:
			writeJSON(w, 202, map[string]any{"queued": true})
		default:
			writeJSON(w, 202, map[string]any{"queued": false, "note": "a test is already queued"})
		}
	})
	mux.HandleFunc("POST /v1/hourly", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Packet") != "1" {
			writeJSON(w, 403, map[string]any{"error": "missing X-Packet header"})
			return
		}
		on := r.URL.Query().Get("on")
		if on != "1" && on != "0" {
			writeJSON(w, 400, map[string]any{"error": "on must be 1 or 0"})
			return
		}
		if err := sampler.SetHourly(on == "1"); err != nil {
			slog.Warn("could not change the hourly setting", "err", err)
			writeJSON(w, 500, map[string]any{"error": "could not change the setting"})
			return
		}
		writeJSON(w, 200, map[string]any{"hourly_test": sampler.HourlyEnabled()})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// runDaemon measures forever and serves the API until ctx ends.
func Run(ctx context.Context) error {
	db, err := store.OpenStore()
	if err != nil {
		return err
	}
	defer db.Close()
	s := &Server{db: db, testNow: make(chan struct{}, 1)}
	ln, err := net.Listen("tcp4", ListenAddr())
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	slog.Info("packet daemon started", "version", version.String(), "addr", ln.Addr().String(), "data", store.DataDir())
	probe.PathStart()
	sampler.Run(ctx, db, s.testNow, s.notify)
	slog.Info("packet daemon stopping")
	shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}
