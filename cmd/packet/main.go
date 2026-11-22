// Command packet: is it the network? Measures each layer of the connection every minute and says
// which one is to blame. `packet daemon` runs the measuring loop and a localhost API that a menu bar app
// reads; the other subcommands answer from the command line.
//
// A Go port of github.com/danielmiessler/packet (MIT).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/cleonte/go-packet/internal/clock"
	"github.com/cleonte/go-packet/internal/history"
	"github.com/cleonte/go-packet/internal/probe"
	"github.com/cleonte/go-packet/internal/report"
	"github.com/cleonte/go-packet/internal/sampler"
	"github.com/cleonte/go-packet/internal/server"
	"github.com/cleonte/go-packet/internal/store"
	"github.com/cleonte/go-packet/internal/verdict"
	"github.com/cleonte/go-packet/internal/version"
)

const help = `packet — is it the network?

USAGE
  packet daemon                   measure every minute and serve the local API (127.0.0.1)
  packet status [--json]          what the running daemon last measured
  packet now [--json]             measure every layer now
  packet test [--json]            measure now plus a full bandwidth test
  packet at <WHEN> [--json]       what the network was like at a past moment
  packet history [--range R] [--json]
  packet hourly on|off            turn the hourly line-speed test on or off (about 3.4 GB/day)
  packet version                  which build this is
  packet demo DIR                 write a made-up day of data into DIR (screenshots, UI work)
  packet help

WHEN   14:30 · 2026-10-12 14:30 · 30m · 2h · 3d · epoch ms
RANGE  1h · 24h (default) · 7d · 30d

EXIT
  0 ok · 1 no data for that time · 2 bad arguments`

// setupLogging sends logs to stderr. PACKET_LOG (debug, info, warn, error) sets the level; the
// default is info for the daemon and warn for one-shot commands, so their output stays clean.
func setupLogging(daemon bool) {
	level := slog.LevelWarn
	if daemon {
		level = slog.LevelInfo
	}
	switch strings.ToLower(os.Getenv("PACKET_LOG")) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
}

func main() {
	var rest []string
	jsonOut := false
	for _, a := range os.Args[1:] {
		switch {
		case a == "--json":
			jsonOut = true
		case strings.HasPrefix(a, "-psn_"):
		default:
			rest = append(rest, a)
		}
	}
	if len(rest) == 0 {
		fmt.Println(help)
		return
	}
	setupLogging(rest[0] == "daemon")
	code := 0
	switch rest[0] {
	case "daemon":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := server.Run(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "packet:", err)
			code = 1
		}
	case "status":
		code = cmdStatus(jsonOut)
	case "now":
		code = cmdNow(jsonOut, false)
	case "test":
		code = cmdNow(jsonOut, true)
	case "at":
		if len(rest) < 2 {
			fmt.Fprintf(os.Stderr, "at needs a time\n\n%s\n", help)
			code = 2
		} else if c, ok := cmdAt(strings.Join(rest[1:], " "), jsonOut); ok {
			code = c
		} else {
			fmt.Fprintf(os.Stderr, "can't read the time %q\n\n%s\n", rest[1], help)
			code = 2
		}
	case "history":
		rng := "24h"
		for i, a := range rest {
			if a == "--range" && i+1 < len(rest) {
				rng = rest[i+1]
			}
		}
		if _, ok := history.RangeMs(rng); !ok {
			fmt.Fprintf(os.Stderr, "unknown range %q\n\n%s\n", rng, help)
			code = 2
		} else {
			code = cmdHistory(rng, jsonOut)
		}
	case "hourly":
		if len(rest) < 2 || (rest[1] != "on" && rest[1] != "off") {
			fmt.Fprintf(os.Stderr, "hourly needs on or off\n")
			code = 2
		} else if err := sampler.SetHourly(rest[1] == "on"); err != nil {
			fmt.Fprintln(os.Stderr, "packet:", err)
			code = 1
		} else {
			fmt.Println("hourly line-speed test:", rest[1])
		}
	case "demo":
		code = cmdDemo(rest[1:])
	case "version", "--version":
		fmt.Println("packet", version.String())
	case "help", "--help", "-h":
		fmt.Println(help)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n%s\n", rest[0], help)
		code = 2
	}
	os.Exit(code)
}

func printText(s sampler.Snapshot, now int64) {
	fmt.Printf("%s %s\n\n", s.Verdict.Level.Mark(), s.Verdict.Sentence)
	fmt.Printf("  %s %s\n", s.Findings[0].Level.Mark(), report.LinkRow(s.Round))
	fmt.Printf("  %s %s\n", s.Findings[1].Level.Mark(), report.EdgeRow(s.Round))
	fmt.Printf("  %s %s\n", s.Findings[2].Level.Mark(), report.DnsRow(s.Round))
	fmt.Printf("  %s %s\n", s.Findings[3].Level.Mark(), report.WorldRow(s.Round))
	fmt.Printf("       %s\n", report.RegionLine(s.Round))
	fmt.Printf("  %s\n", report.BandwidthRow(s.Full, now))
}

func printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

func cmdNow(jsonOut, full bool) int {
	at := clock.NowMs()
	db, _ := store.OpenStore()
	var base verdict.Baseline
	var recent *float64
	var bw *probe.Bandwidth
	if db != nil {
		defer db.Close()
		base, recent, bw = store.LoadBaseline(db, at), store.RecentScheduledDown(db, at), store.LatestFull(db)
	}
	r := probe.RunRound(at, uint64(at/60_000))
	if full {
		b := probe.MeasureBandwidth(probe.TrigManual, clock.NowMs())
		bw = &b
	}
	v, fs := verdict.Judge(r, base, recent), verdict.Findings(r, base, recent)
	s := sampler.Snapshot{Round: r, Verdict: v, Findings: fs, Full: bw}
	if jsonOut {
		printJSON(report.SnapshotJSON(s, clock.NowMs()))
	} else {
		printText(s, clock.NowMs())
	}
	return 0
}

func cmdAt(when string, jsonOut bool) (int, bool) {
	now := clock.NowMs()
	t, ok := clock.ParseWhen(when, now)
	if !ok {
		return 0, false
	}
	db, err := store.OpenStore()
	if err != nil {
		return 1, true
	}
	defer db.Close()
	s, _ := store.NearestRound(db, t, 5*60_000)
	if s == nil {
		if jsonOut {
			printJSON(map[string]any{"asked": clock.ISOLocal(t), "found": false})
		} else {
			fmt.Printf("No measurement within 5 minutes of %s.\n", clock.ISOLocal(t))
		}
		return 1, true
	}
	around, _ := store.RoundsBetween(db, t-10*60_000, t+10*60_000)
	bad, worst := 0, verdict.Good
	for _, x := range around {
		if x.Verdict.Level >= verdict.Fair {
			bad++
		}
		worst = max(worst, x.Verdict.Level)
	}
	bw := store.NearestBandwidth(db, t, 15*60_000)
	fs := verdict.Findings(s.Round, store.LoadBaseline(db, t), nil)
	if jsonOut {
		out := report.RoundJSON(s.Round, s.Verdict)
		out["asked"], out["found"] = clock.ISOLocal(t), true
		out["window"] = map[string]any{"minutes": 20, "rounds": len(around), "bad_rounds": bad, "worst": worst.Slug()}
		out["bandwidth"] = report.BwJSON(bw)
		printJSON(out)
		return 0, true
	}
	fmt.Printf("At %s (nearest measurement %s):\n", clock.HHMM(t), clock.HHMM(s.Round.AtMs))
	printText(sampler.Snapshot{Round: s.Round, Verdict: s.Verdict, Findings: fs, Full: bw}, now)
	fmt.Printf("\n  ±10 min: %d of %d rounds were fair or worse (worst: %s).\n", bad, len(around), worst.Slug())
	return 0, true
}

func cmdHistory(rng string, jsonOut bool) int {
	db, err := store.OpenStore()
	if err != nil {
		return 1
	}
	defer db.Close()
	h, err := history.Build(db, rng, clock.NowMs())
	if err != nil {
		return 1
	}
	if jsonOut {
		b, _ := json.Marshal(h)
		fmt.Println(string(b))
		return 0
	}
	fmt.Printf("Last %s: %v rounds, %v fair or worse.\n", rng, h["rounds"], h["bad_rounds"])
	for _, b := range h["buckets"].([]map[string]any) {
		if b["level"] != "good" {
			fmt.Printf("  %s  %-8s %s\n", clock.HHMM(b["t"].(int64)), b["level"], b["layer"])
		}
	}
	return 0
}

// cmdStatus asks the running daemon.
func cmdStatus(jsonOut bool) int {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + server.ListenAddr() + "/v1/status")
	if err != nil {
		fmt.Fprintln(os.Stderr, "no daemon answering; start it with `packet daemon`")
		return 1
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if jsonOut {
		fmt.Println(string(body))
		return 0
	}
	var v map[string]any
	if json.Unmarshal(body, &v) != nil || v["ready"] != true {
		fmt.Println("daemon is up, first round not finished yet")
		return 0
	}
	verdict := v["verdict"].(map[string]any)
	marks := map[string]string{"good": "🟢", "fair": "🟡", "poor": "🔴", "offline": "⚫"}
	fmt.Printf("%s %v\n\n", marks[verdict["level"].(string)], verdict["sentence"])
	for _, r := range v["rows"].([]any) {
		row := r.(map[string]any)
		fmt.Printf("  %s %v\n", marks[row["level"].(string)], row["text"])
	}
	fmt.Printf("  %v\n", v["line_speed_row"])
	return 0
}

// cmdDemo writes a synthetic day into a fresh directory. It refuses the real data directory and
// any directory that already holds a database, so it can never touch your history.
func cmdDemo(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "demo needs a directory: packet demo DIR")
		return 2
	}
	dir, err := filepath.Abs(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "packet:", err)
		return 2
	}
	if real, _ := filepath.Abs(store.DataDir()); dir == real {
		fmt.Fprintln(os.Stderr, "packet: refusing to write demo data into the real data directory")
		return 2
	}
	path := filepath.Join(dir, "packet.sqlite")
	if _, err := os.Stat(path); err == nil {
		fmt.Fprintln(os.Stderr, "packet: refusing to overwrite", path)
		return 2
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "packet:", err)
		return 1
	}
	db, err := store.OpenAt(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "packet:", err)
		return 1
	}
	defer db.Close()
	if err := store.WriteDemo(db, clock.NowMs()); err != nil {
		fmt.Fprintln(os.Stderr, "packet:", err)
		return 1
	}
	fmt.Printf("wrote a made-up day to %s\nview it with:\n  PACKET_DATA_DIR=%s PACKET_PORT=47999 packet daemon\n  open http://127.0.0.1:47999/\n", path, dir)
	return 0
}
