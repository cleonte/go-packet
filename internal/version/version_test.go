package version

import "testing"

func TestStringIsNeverEmpty(t *testing.T) {
	if String() == "" {
		t.Fatal("empty version")
	}
}

func TestLinkerOverrideWins(t *testing.T) {
	old := Version
	defer func() { Version = old }()
	Version = "v9.9.9"
	if got := String(); got != "v9.9.9" {
		t.Fatalf("got %q", got)
	}
}
