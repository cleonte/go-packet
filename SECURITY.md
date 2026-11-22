# Security

## Reporting a problem

Please report security issues **privately**, not in a public issue: use
[Report a vulnerability](https://github.com/cleonte/go-packet/security/advisories/new) on the
Security tab. I'll answer as soon as I can.

## What is in scope

`packet daemon` listens on `127.0.0.1` only. The things worth reporting:

- A way for a web page, another user, or another machine to reach the API, or to make it change
  state (start a test, change the hourly setting). The API refuses non-loopback `Host` headers and
  requires an `X-Packet: 1` header on every `POST`; a bypass of either is a bug.
- Anything that makes `packet` read, write or send data beyond what the README lists: it records
  numbers about the network path and nothing about your traffic.
- Reading or writing outside the data directory (`~/Library/Application Support/Packet`, or
  `PACKET_DATA_DIR`), including through symlinks.

The history is stored owner-only (`0700` directory, `0600` files).

## Supported versions

Only the latest release.
