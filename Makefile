# Standard Go layout: cmd/packet is the binary, internal/* the packages.
# `make install` puts the CLI in ~/.local/bin.
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
