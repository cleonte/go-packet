# packet-go

**Is it the network?** A Go port of [danielmiessler/packet](https://github.com/danielmiessler/packet)
(MIT, see [LICENSE](LICENSE)). It measures every layer once a minute and names the one to blame:
Wi-Fi, local network, internet provider, DNS, or far internet. Same measurements, thresholds and
storage schema as the original, so either can read the other's database.

It has no tray icon or window of its own: `packet daemon` serves a localhost API, and
[CLBar](../CLBar) is the UI. A small history page is served at `http://127.0.0.1:47831/`.

## Build

```bash
make build     # ./packet
make test      # go test -race
```

CGO is used for two small Objective-C bridges (`native_darwin.m`): CoreWLAN for Wi-Fi signal, and
Network.framework for expensive/constrained paths. Everything else is pure Go; SQLite is
`modernc.org/sqlite`.

## Use

```
packet daemon                   measure every minute and serve the local API (127.0.0.1)
packet status [--json]          what the running daemon last measured
packet now [--json]             measure every layer now
packet test [--json]            measure now plus a full bandwidth test
packet at <WHEN> [--json]       what the network was like then: 14:30, 2026-10-12 14:30, 30m, 2h, 3d
packet history [--range R] [--json]
packet hourly on|off            hourly line-speed test (about 3.4 GB/day, off by default)
```

Data lives in `~/Library/Application Support/Packet/packet.sqlite` (90 days); `PACKET_DATA_DIR`
overrides it, and `PACKET_PORT` changes the API port.

## API (127.0.0.1 only)

| | |
|---|---|
| `GET /v1/status` | latest round, verdict, per-layer rows, bandwidth |
| `GET /v1/history?range=1h\|24h\|7d\|30d` | bucketed history |
| `POST /v1/test` | run Test Now |
| `POST /v1/hourly?on=1\|0` | toggle the hourly test |
| `GET /v1/health` | liveness |

POSTs need an `X-Packet: 1` header and every request needs a loopback `Host`, which blocks
cross-site and DNS-rebinding requests from web pages.

## Running it

Run the daemon from CLBar (it starts it for you), or from a terminal. A launchd agent
(`packet agent install`) works but **cannot ping the router**: macOS Local Network privacy blocks
bare launchd binaries. The Internet, DNS and World layers are unaffected.

## Differences from the original

- No tray icon, window or login-item code; CLBar is the UI.
- Bandwidth tests use Go's `net/http` instead of shelling out to `curl`.
- `packet status` and the daemon API are new.
