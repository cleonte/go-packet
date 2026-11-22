# Contributing

Thanks for looking. This is a small project; issues and pull requests are welcome.

## Build and test

You need macOS 14+, the Xcode Command Line Tools and Go.

```bash
make test    # go vet + go test -race
make build   # bin/packet
```

CI runs the same on a macOS runner (the code uses CoreWLAN and Network.framework).

## Try your change without touching real data

```bash
bin/packet demo /tmp/demo
PACKET_DATA_DIR=/tmp/demo PACKET_PORT=47999 PACKET_LOG=debug bin/packet daemon
```

`PACKET_DATA_DIR` keeps your real history out of it, and `PACKET_PORT` avoids a running daemon.

## Layout

`cmd/packet` is the binary and `internal/*` the packages; dependencies point one way (see the
README). Keep it that way: `probe` knows nothing of `verdict`, `verdict` nothing of `store`.

## Pull requests

- Keep them small and say why in the description.
- New behavior gets a test. The HTTP API is localhost-only and its checks (loopback Host, the
  `X-Packet` header on POSTs) are covered by tests: please keep them passing.
- Security problems: please report privately, see [SECURITY.md](SECURITY.md).

## Credit

This is a port of [danielmiessler/packet](https://github.com/danielmiessler/packet) (MIT). Changes
that make it diverge from the original's measurements or schema should say so in the README.
