package main

// Plain-words rows shared by the daemon's status and the CLI.

import (
	"fmt"
	"strings"
)

func lossStr(v *float64) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf(" · %.0f%% loss", *v*100)
}

func linkRow(r *Round) string {
	router := "no router"
	switch {
	case r.Gateway.Ms != nil:
		router = fmt.Sprintf("router %s%s", fmtMs(*r.Gateway.Ms), lossStr(r.Gateway.Loss))
	case r.GatewayKnown:
		router = "router doesn't answer pings"
	}
	switch {
	case r.Link == LinkWiFi && r.WiFi != nil:
		w := r.WiFi
		band := ""
		if l := w.BandLabel(); l != "" {
			band = fmt.Sprintf(" · %s ch %d", l, w.Channel)
		}
		return fmt.Sprintf("Wi-Fi %d dBm · %.0f Mbps%s · %s", w.Rssi, w.TxMbps, band, router)
	case r.Link == LinkNone:
		return "No network connection"
	}
	return fmt.Sprintf("%s · %s", r.Link.Label(), router)
}

func edgeRow(r *Round) string {
	if r.Edge.Ms == nil {
		return "Internet · Cloudflare unreachable"
	}
	colo := ""
	if r.Colo != "" {
		colo = " " + r.Colo
	}
	jitter := ""
	if r.Edge.Jitter != nil {
		jitter = " · jitter " + fmtMs(*r.Edge.Jitter)
	}
	return fmt.Sprintf("Internet · Cloudflare%s %s%s%s", colo, fmtMs(*r.Edge.Ms), lossStr(r.Edge.Loss), jitter)
}

func dnsRow(r *Round) string {
	if r.DnsMs == nil {
		return "DNS lookups failing"
	}
	return "DNS lookups " + fmtMs(*r.DnsMs)
}

func worldRow(r *Round) string {
	m := r.FarMedian()
	if m == nil {
		return "World · no region reachable"
	}
	return fmt.Sprintf("World · %d of %d regions · median %s", r.RegionsUp(), len(Regions), fmtMs(*m))
}

func regionLine(r *Round) string {
	parts := make([]string, len(Regions))
	for i, g := range Regions {
		v := "—"
		if r.Regions[i] != nil {
			v = fmt.Sprintf("%.0f", *r.Regions[i])
		}
		parts[i] = g.Code + " " + v
	}
	return strings.Join(parts, " · ") + " ms"
}

func mbps(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.0f", *v)
}

func lineSpeedRow(b *Bandwidth, now int64) string {
	if b == nil {
		return "Line speed · not measured yet: use Test Now"
	}
	if b.Rpm != nil {
		return fmt.Sprintf("Capacity ⬇ %s ⬆ %s Mbps · responsiveness %s (%.0f RPM) · Test Now %s",
			mbps(b.DownMbps), mbps(b.UpMbps), rpmLabel(*b.Rpm), *b.Rpm, ago(now, b.AtMs))
	}
	kind := "hourly test"
	if b.Trigger == TrigManual {
		kind = "Test Now"
	}
	return fmt.Sprintf("Cloudflare ⬇ %s ⬆ %s Mbps · %s %s", mbps(b.DownMbps), mbps(b.UpMbps), kind, ago(now, b.AtMs))
}

// bandwidthRow is any test, labelled with its kind (used by `packet at`).
func bandwidthRow(b *Bandwidth, now int64) string {
	switch {
	case b == nil:
		return "Bandwidth · not measured yet"
	case b.Trigger.IsFull():
		return lineSpeedRow(b, now)
	}
	return fmt.Sprintf("Quick check ⬇ %s ⬆ %s Mbps (reads low on fast lines) · %s", mbps(b.DownMbps), mbps(b.UpMbps), ago(now, b.AtMs))
}

// quickRow is the quick check against its own normal.
func quickRow(q *Bandwidth, sh *float64, now int64) string {
	switch {
	case q == nil:
		return "Quick check · every 10 minutes"
	case sh == nil:
		return "Quick check · learning what's normal · " + ago(now, q.AtMs)
	case *sh < 0.25:
		return fmt.Sprintf("Quick check · download %.0f%% of usual · %s", *sh*100, ago(now, q.AtMs))
	}
	return "Quick check · normal · " + ago(now, q.AtMs)
}

func bwJSON(b *Bandwidth) any {
	if b == nil {
		return nil
	}
	return map[string]any{"at": isoLocal(b.AtMs), "down_mbps": round1(b.DownMbps), "up_mbps": round1(b.UpMbps),
		"trigger": b.Trigger.Slug(), "responsiveness_rpm": round1(b.Rpm), "bytes": b.DownBytes + b.UpBytes}
}

func findingsJSON(fs [4]Finding) []map[string]any {
	names := [4]string{"link", "internet", "dns", "world"}
	out := make([]map[string]any, 4)
	for i, f := range fs {
		out[i] = map[string]any{"layer": names[i], "level": f.Level.Slug(), "detail": f.Detail}
	}
	return out
}

// snapshotJSON is what the daemon serves and the CLI prints: the round, the verdict, per-layer
// findings and the plain-words rows a UI can show as they are.
func snapshotJSON(s Snapshot, now int64) map[string]any {
	out := s.Round.JSON(s.Verdict)
	out["layers"] = findingsJSON(s.Findings)
	out["rows"] = []map[string]any{
		{"layer": "link", "level": s.Findings[0].Level.Slug(), "text": linkRow(s.Round)},
		{"layer": "internet", "level": s.Findings[1].Level.Slug(), "text": edgeRow(s.Round)},
		{"layer": "dns", "level": s.Findings[2].Level.Slug(), "text": dnsRow(s.Round)},
		{"layer": "world", "level": s.Findings[3].Level.Slug(), "text": worldRow(s.Round), "detail": regionLine(s.Round)},
	}
	out["bandwidth"] = bwJSON(s.Full)
	out["line_speed_row"] = lineSpeedRow(s.Full, now)
	out["quick"] = bwJSON(s.Quick)
	out["quick_row"] = quickRow(s.Quick, s.QuickShare, now)
	out["testing"] = s.Testing
	return out
}
