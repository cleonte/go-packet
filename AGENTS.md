# AGENTS.md

Guidance for AI coding agents (and a quick orientation for people) working on go-packet. Keep this
short: the README, CONTRIBUTING.md and SECURITY.md have the rest.

## What this is

A headless macOS daemon and CLI that measures each layer of the connection every minute (link,
router, internet, DNS, far regions) and names the layer to blame. It is a Go port of
[danielmiessler/packet](https://github.com/danielmiessler/packet) (MIT). Keep the credit in the
README, his notice in `LICENSE`, and the SQLite schema compatible with the original.

## Commands

```bash
make test    # go vet + go test -race: run before every commit
make build   # bin/packet

bin/packet demo /tmp/demo   # a made-up day of data, never touches real history
PACKET_DATA_DIR=/tmp/demo PACKET_PORT=47999 PACKET_LOG=debug bin/packet daemon
```

macOS only: `internal/probe/native_darwin.*` uses cgo (CoreWLAN, Network.framework, IOKit), so it
will not build elsewhere.

## Layout and the dependency rule

`cmd/packet` is the binary; `internal/*` are the packages (see the README for the list).
Dependencies point one way: `probe` imports nothing of ours, `verdict` imports `probe`, `store`
imports both, then `sampler`, `report`, `history`, `server`, and last `cmd/packet`. Never add an
import that points back up. (That is why the JSON builders live in `report`, not `probe`.)

Tests sit beside the code. `internal/probe/probetest` has the shared fixture. Package-level
variables in `probe` and `sampler` (URLs, `measureRound`, `powerState`, timings) are seams so tests
can stub the network, power and clocks: use them rather than adding real network calls to tests.

## Rules that are easy to break

1. **The API is localhost-only.** Every request needs a loopback `Host`, and every POST needs an
   `X-Packet: 1` header. Do not weaken these; `internal/server/server_test.go` covers them.
2. **Run the daemon as a child of an app or a terminal, never as a launchd agent.** macOS Local
   Network privacy blocks bare launchd binaries from pinging the router.
3. **Back off when power matters.** On battery and in Low Power Mode background bandwidth checks
   pause, and rounds slow down in Low Power Mode (`internal/sampler/power.go`). Wake from sleep and
   path changes trigger an early round, and a bad round right after a wake is measured once more.
   Keep these behaviors and their tests.
4. **Record numbers about the path, nothing about the user's traffic.** No hostnames visited, no Wi-Fi
   network names, no apps. Do not add a column that could hold them, and never put real network data
   in tests, fixtures, logs or screenshots.
5. **The list of hosts contacted is in the README.** Adding one needs a reason and a README update.
6. **Only 2xx answers count as bandwidth measurements.**

## Releases and git

- A release is a `vX.Y.Z` tag plus a GitHub release with notes. Consumers pin the tag
  (`go install ...@vX.Y.Z`). **Do not tag, publish a release, push, or change repo settings unless
  the maintainer asks in the current session.**
- `master` has light branch protection (CI checks `test` and `analyze`). CI runs on macOS and
  includes govulncheck, staticcheck and CodeQL.
- Do not change the configured git identity.
