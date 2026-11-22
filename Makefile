# Standard Go layout: cmd/packet is the binary, internal/* the packages.
# `make install` puts the CLI in ~/.local/bin. The daemon is started by CLBar
# (macOS blocks LAN access for bare launchd binaries, so no launchd agent; see README).
BIN := $(HOME)/.local/bin/packet

build:
	go build -ldflags="-s -w" -o bin/packet ./cmd/packet

test:
	go vet ./...
	go test -race ./...

install: test build
	mkdir -p $(dir $(BIN))
	cp bin/packet $(BIN)

uninstall:
	rm -f $(BIN)

.PHONY: build test install uninstall
