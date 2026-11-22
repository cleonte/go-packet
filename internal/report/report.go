// Package report turns rounds into plain-words rows and JSON. plain-words rows shared by the daemon's status and the CLI.
package report

import (
	"fmt"
	"strings"

	"github.com/cleonte/go-packet/internal/clock"
	"github.com/cleonte/go-packet/internal/num"
	"github.com/cleonte/go-packet/internal/probe"
	"github.com/cleonte/go-packet/internal/sampler"
	"github.com/cleonte/go-packet/internal/verdict"
)

func lossStr(v *float64) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf(" · %.0f%% loss", *v*100)
}

func LinkRow(r *probe.Round) string {
	router := "no router"
	switch {
	case r.Gateway.Ms != nil:
		router = fmt.Sprintf("router %s%s", verdict.FmtMs(*r.Gateway.Ms), lossStr(r.Gateway.Loss))
	case r.GatewayKnown:
		router = "router doesn't answer pings"
	}
	switch {
	case r.Link == probe.LinkWiFi && r.WiFi != nil:
		w := r.WiFi
		band := ""
		if l := w.BandLabel(); l != "" {
			band = fmt.Sprintf(" · %s ch %d", l, w.Channel)
		}
		return fmt.Sprintf("Wi-Fi %d dBm · %.0f Mbps%s · %s", w.Rssi, w.TxMbps, band, router)
	case r.Link == probe.LinkNone:
		return "No network connection"
	}
	return fmt.Sprintf("%s · %s", r.Link.Label(), router)
}

func EdgeRow(r *probe.Round) string {
	if r.Edge.Ms == nil {
		return "Internet · Cloudflare unreachable"
	}
	colo := ""
	if r.Colo != "" {
		colo = " " + r.Colo
	}
	jitter := ""
	if r.Edge.Jitter != nil {
		jitter = " · jitter " + verdict.FmtMs(*r.Edge.Jitter)
	}
	return fmt.Sprintf("Internet · Cloudflare%s %s%s%s", colo, verdict.FmtMs(*r.Edge.Ms), lossStr(r.Edge.Loss), jitter)
}

func DnsRow(r *probe.Round) string {
	if r.DnsMs == nil {
		return "DNS lookups failing"
	}
	return "DNS lookups " + verdict.FmtMs(*r.DnsMs)
}

func WorldRow(r *probe.Round) string {
	m := r.FarMedian()
	if m == nil {
		return "World · no region reachable"
	}
	return fmt.Sprintf("World · %d of %d regions · median %s", r.RegionsUp(), len(probe.Regions), verdict.FmtMs(*m))
}

func RegionLine(r *probe.Round) string {
	parts := make([]string, len(probe.Regions))
	for i, g := range probe.Regions {
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

func lineSpeedRow(b *probe.Bandwidth, now int64) string {
	if b == nil {
		return "Line speed · not measured yet: use Test Now"
	}
	if b.Rpm != nil {
		return fmt.Sprintf("Capacity ⬇ %s ⬆ %s Mbps · responsiveness %s (%.0f RPM) · Test Now %s",
			mbps(b.DownMbps), mbps(b.UpMbps), probe.RpmLabel(*b.Rpm), *b.Rpm, clock.Ago(now, b.AtMs))
	}
	kind := "hourly test"
	if b.Trigger == probe.TrigManual {
		kind = "Test Now"
	}
	return fmt.Sprintf("Cloudflare ⬇ %s ⬆ %s Mbps · %s %s", mbps(b.DownMbps), mbps(b.UpMbps), kind, clock.Ago(now, b.AtMs))
}

// bandwidthRow is any test, labelled with its kind (used by `packet at`).
func BandwidthRow(b *probe.Bandwidth, now int64) string {
	switch {
	case b == nil:
		return "Bandwidth · not measured yet"
	case b.Trigger.IsFull():
		return lineSpeedRow(b, now)
	}
	return fmt.Sprintf("Quick check ⬇ %s ⬆ %s Mbps (reads low on fast lines) · %s", mbps(b.DownMbps), mbps(b.UpMbps), clock.Ago(now, b.AtMs))
}

// quickRow is the quick check against its own normal.
func quickRow(q *probe.Bandwidth, sh *float64, now int64) string {
	switch {
	case q == nil:
		return "Quick check · every 10 minutes"
	case sh == nil:
		return "Quick check · learning what's normal · " + clock.Ago(now, q.AtMs)
	case *sh < 0.25:
		return fmt.Sprintf("Quick check · download %.0f%% of usual · %s", *sh*100, clock.Ago(now, q.AtMs))
	}
	return "Quick check · normal · " + clock.Ago(now, q.AtMs)
}

func BwJSON(b *probe.Bandwidth) any {
	if b == nil {
		return nil
	}
	return map[string]any{"at": clock.ISOLocal(b.AtMs), "down_mbps": num.Round1(b.DownMbps), "up_mbps": num.Round1(b.UpMbps),
		"trigger": b.Trigger.Slug(), "responsiveness_rpm": num.Round1(b.Rpm), "bytes": b.DownBytes + b.UpBytes}
}

func findingsJSON(fs [4]verdict.Finding) []map[string]any {
	names := [4]string{"link", "internet", "dns", "world"}
	out := make([]map[string]any, 4)
	for i, f := range fs {
		out[i] = map[string]any{"layer": names[i], "level": f.Level.Slug(), "detail": f.Detail}
	}
	return out
}

// snapshotJSON is what the daemon serves and the CLI prints: the round, the verdict, per-layer
// findings and the plain-words rows a UI can show as they are.
func SnapshotJSON(s sampler.Snapshot, now int64) map[string]any {
	out := RoundJSON(s.Round, s.Verdict)
	out["layers"] = findingsJSON(s.Findings)
	out["rows"] = []map[string]any{
		{"layer": "link", "level": s.Findings[0].Level.Slug(), "text": LinkRow(s.Round), "short": linkShort(s.Round)},
		{"layer": "internet", "level": s.Findings[1].Level.Slug(), "text": EdgeRow(s.Round), "short": edgeShort(s.Round)},
		{"layer": "dns", "level": s.Findings[2].Level.Slug(), "text": DnsRow(s.Round), "short": dnsShort(s.Round)},
		{"layer": "world", "level": s.Findings[3].Level.Slug(), "text": WorldRow(s.Round), "short": worldShort(s.Round), "detail": RegionLine(s.Round)},
	}
	out["bandwidth"] = BwJSON(s.Full)
	out["line_speed_row"] = lineSpeedRow(s.Full, now)
	out["line_speed_short"] = speedShort(s.Full, now)
	out["quick"] = BwJSON(s.Quick)
	out["quick_row"] = quickRow(s.Quick, s.QuickShare, now)
	out["testing"] = s.Testing
	return out
}

// Short rows for narrow UIs (menu bar dropdowns). Loss is shown only when there is some.

func lossShort(v *float64) string {
	if v == nil || *v < 0.005 {
		return ""
	}
	return fmt.Sprintf(" · %.0f%% loss", *v*100)
}

func linkShort(r *probe.Round) string {
	router := ""
	switch {
	case r.Gateway.Ms != nil:
		router = fmt.Sprintf(" · router %s%s", verdict.FmtMs(*r.Gateway.Ms), lossShort(r.Gateway.Loss))
	case r.GatewayKnown:
		router = " · router silent"
	}
	switch {
	case r.Link == probe.LinkWiFi && r.WiFi != nil:
		band := ""
		if l := r.WiFi.BandLabel(); l != "" {
			band = " · " + l
		}
		return fmt.Sprintf("Wi-Fi %d dBm%s%s", r.WiFi.Rssi, band, router)
	case r.Link == probe.LinkNone:
		return "No network connection"
	}
	return r.Link.Label() + router
}

func edgeShort(r *probe.Round) string {
	if r.Edge.Ms == nil {
		return "Cloudflare unreachable"
	}
	colo := ""
	if r.Colo != "" {
		colo = " " + r.Colo
	}
	return fmt.Sprintf("Cloudflare%s %s%s", colo, verdict.FmtMs(*r.Edge.Ms), lossShort(r.Edge.Loss))
}

func dnsShort(r *probe.Round) string {
	if r.DnsMs == nil {
		return "DNS failing"
	}
	return "DNS " + verdict.FmtMs(*r.DnsMs)
}

func worldShort(r *probe.Round) string {
	m := r.FarMedian()
	if m == nil {
		return "World · none reachable"
	}
	return fmt.Sprintf("World %d/%d regions · median %s", r.RegionsUp(), len(probe.Regions), verdict.FmtMs(*m))
}

func speedShort(b *probe.Bandwidth, now int64) string {
	if b == nil {
		return "Speed not measured · use Test Now"
	}
	rpm := ""
	if b.Rpm != nil {
		rpm = fmt.Sprintf(" · %s (%.0f RPM)", probe.RpmLabel(*b.Rpm), *b.Rpm)
	}
	return fmt.Sprintf("⬇ %s ⬆ %s Mbps%s · %s", mbps(b.DownMbps), mbps(b.UpMbps), rpm, clock.Ago(now, b.AtMs))
}

func layerJSON(l probe.Layer) map[string]any {
	var loss any
	if l.Loss != nil {
		loss = float64(int64(*l.Loss*1000+0.5)) / 1000
	}
	return map[string]any{"ms": num.Round1(l.Ms), "loss": loss, "jitter_ms": num.Round1(l.Jitter)}
}

// RoundJSON is the round and its verdict as the API and `--json` print them.
func RoundJSON(r *probe.Round, v verdict.Verdict) map[string]any {
	regions := make([]map[string]any, 8)
	for i, g := range probe.Regions {
		regions[i] = map[string]any{"code": g.Code, "name": g.Name, "continent": g.Continent, "ms": num.Round1(r.Regions[i])}
	}
	link := map[string]any{"kind": r.Link.Slug(), "label": r.Link.Label(), "wifi": nil}
	if w := r.WiFi; w != nil {
		link["wifi"] = map[string]any{"rssi_dbm": w.Rssi, "noise_dbm": w.Noise, "snr_db": w.SNR(),
			"tx_mbps": w.TxMbps, "channel": w.Channel, "band": w.BandLabel()}
	}
	var colo any
	if r.Colo != "" {
		colo = r.Colo
	}
	return map[string]any{
		"at": clock.ISOLocal(r.AtMs), "at_ms": r.AtMs, "verdict": v.JSON(), "link": link,
		"gateway": layerJSON(r.Gateway),
		"edge":    map[string]any{"colo": colo, "ms": num.Round1(r.Edge.Ms), "loss": r.Edge.Loss, "jitter_ms": num.Round1(r.Edge.Jitter)},
		"dns":     map[string]any{"ms": num.Round1(r.DnsMs)},
		"regions": regions, "path_expensive": r.Expensive,
	}
}
