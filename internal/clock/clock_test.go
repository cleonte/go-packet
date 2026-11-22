package clock

import (
	"testing"
	"time"
)

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
		if got, good := ParseWhen(c.in, now); !good || got != c.want {
			t.Errorf("parseWhen(%q)=%v,%v want %v", c.in, got, good, c.want)
		}
	}
	for _, bad := range []string{"25:00", "2026-02-30 14:30", "soon", "2026-10-12 1é:30"} {
		if _, good := ParseWhen(bad, now); good {
			t.Errorf("parseWhen(%q) should fail", bad)
		}
	}
	first := at(2026, 11, 1, 9, 0)
	if got, _ := ParseWhen("10:00", first); got != at(2026, 10, 31, 10, 0) {
		t.Error("yesterday across a month end")
	}
}
