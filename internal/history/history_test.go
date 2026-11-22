package history

import (
	"path/filepath"
	"testing"

	"github.com/cleonte/go-packet/internal/clock"
	"github.com/cleonte/go-packet/internal/probe"
	"github.com/cleonte/go-packet/internal/probe/probetest"
	"github.com/cleonte/go-packet/internal/store"
	"github.com/cleonte/go-packet/internal/verdict"
)

func TestBuildCountsRoundsAndBadOnes(t *testing.T) {
	db, err := store.OpenAt(filepath.Join(t.TempDir(), "h.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	base := clock.NowMs() - 3_600_000
	for i := 0; i < 40; i++ {
		r := probetest.Healthy()
		r.AtMs = base + int64(i)*60_000
		v := verdict.Verdict{Level: verdict.Good}
		if i >= 30 {
			v = verdict.Verdict{Level: verdict.Fair, At: verdict.WhereDns}
		}
		if err := store.InsertRound(db, r, v); err != nil {
			t.Fatal(err)
		}
	}
	h, err := Build(db, "24h", clock.NowMs())
	if err != nil {
		t.Fatal(err)
	}
	if h["rounds"].(int) != 40 || h["bad_rounds"].(int) != 10 {
		t.Fatalf("rounds=%v bad=%v", h["rounds"], h["bad_rounds"])
	}
	if len(h["buckets"].([]map[string]any)) == 0 {
		t.Fatal("no buckets")
	}
}

func TestBucketLinkIsTheMajority(t *testing.T) {
	cases := []struct {
		in   []probe.LinkKind
		want probe.LinkKind
	}{
		{[]probe.LinkKind{probe.LinkEthernet, probe.LinkEthernet, probe.LinkEthernet, probe.LinkWiFi}, probe.LinkEthernet},
		{[]probe.LinkKind{probe.LinkWiFi, probe.LinkWiFi, probe.LinkEthernet}, probe.LinkWiFi},
		{[]probe.LinkKind{probe.LinkWiFi, probe.LinkEthernet}, probe.LinkEthernet}, // a tie goes to the later link
	}
	for _, c := range cases {
		if got := predominant(c.in); got != c.want {
			t.Errorf("predominant(%v)=%v want %v", c.in, got, c.want)
		}
	}
}
