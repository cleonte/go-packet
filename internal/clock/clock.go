// Package clock has Packet's time helpers: millisecond timestamps, plain-words ages and the
// forgiving parser behind `packet at`.
package clock

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func NowMs() int64 { return time.Now().UnixMilli() }

func ISOLocal(ms int64) string { return time.UnixMilli(ms).Format("2006-01-02T15:04:05-07:00") }

func HHMM(ms int64) string { return time.UnixMilli(ms).Format("15:04") }

// ago: "just now", "4 min ago", "2 h ago".
func Ago(now, then int64) string {
	s := max(now-then, 0) / 1000
	switch {
	case s < 60:
		return "just now"
	case s < 3600:
		return fmt.Sprintf("%d min ago", s/60)
	case s < 172_800:
		return fmt.Sprintf("%d h ago", s/3600)
	}
	return fmt.Sprintf("%d days ago", s/86_400)
}

func hm(s string) (int, int, bool) {
	var h, m int
	if n, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil || n != 2 || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// parseWhen parses `14:30` (the most recent past 14:30), `2026-10-12 14:30`, `2026-10-12T14:30`,
// `30m` / `2h` / `3d` (ago), or epoch milliseconds.
func ParseWhen(s string, now int64) (int64, bool) {
	s = trimSuffix(trim(s), " ago")
	if n, ok := suffixInt(s, "m"); ok {
		return now - n*60_000, true
	}
	if n, ok := suffixInt(s, "h"); ok {
		return now - n*3_600_000, true
	}
	if n, ok := suffixInt(s, "d"); ok {
		return now - n*86_400_000, true
	}
	if len(s) >= 12 {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n, true
		}
	}
	if h, m, ok := hm(s); ok && !contains(s, "-") {
		t := time.UnixMilli(now).In(time.Local)
		at := time.Date(t.Year(), t.Month(), t.Day(), h, m, 0, 0, time.Local)
		if at.UnixMilli() > now {
			at = time.Date(t.Year(), t.Month(), t.Day()-1, h, m, 0, 0, time.Local)
		}
		return at.UnixMilli(), true
	}
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04"} {
		if len(s) >= len(layout) {
			if t, err := time.ParseInLocation(layout, s[:len(layout)], time.Local); err == nil {
				return t.UnixMilli(), true
			}
		}
	}
	return 0, false
}

func trim(s string) string { return strings.TrimSpace(s) }

func trimSuffix(s, suf string) string { return strings.TrimSuffix(s, suf) }

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func suffixInt(s, suf string) (int64, bool) {
	if len(s) <= len(suf) || s[len(s)-len(suf):] != suf {
		return 0, false
	}
	n, err := strconv.ParseInt(s[:len(s)-len(suf)], 10, 64)
	return n, err == nil
}
