// Package version reports which build of packet is running.
package version

import (
	"runtime/debug"
	"strings"
)

// Version can be set at build time:
//
//	go build -ldflags "-X github.com/cleonte/go-packet/internal/version.Version=v1.2.3"
//
// Otherwise String reads what the Go toolchain recorded: the module version for
// `go install ...@v0.1.0`, or the commit for a build from a checkout.
var Version string

// String is the version of this build, e.g. "v0.1.0", or "dev+a1b2c3d-dirty" from a checkout.
func String() string {
	if Version != "" {
		return Version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	rev, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	var b strings.Builder
	b.WriteString("dev")
	if rev != "" {
		b.WriteString("+" + rev[:min(7, len(rev))])
	}
	if dirty {
		b.WriteString("-dirty")
	}
	return b.String()
}
