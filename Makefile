# Builds the Go Packet binary. `make install` puts the CLI in ~/.local/bin.
# The daemon is started by CLBar (a launchd agent cannot ping the router; see README).
BIN := $(HOME)/.local/bin/packet

build:
	go build -ldflags="-s -w" -o packet .

test:
	go test -race ./...

install: test build
	mkdir -p $(dir $(BIN))
	cp packet $(BIN)

uninstall:
	rm -f $(BIN)

.PHONY: build test install uninstall
