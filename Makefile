VERSION ?= $(shell sed -n 's/^version = "\(.*\)"/\1/p' herdr-plugin.toml | head -1)
LDFLAGS := -s -w -X github.com/ellingtonsp/herdr-orch/internal/daemon.Version=v$(VERSION)
GOFLAGS := -trimpath
OUT ?= bin

.PHONY: build test race vet install-cli link integration clean

build:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(OUT)/herdr-orch ./cmd/herdr-orch
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(OUT)/horch ./cmd/horch

vet:
	go vet ./...

test:
	go test ./...

race:
	go test -race -count=1 ./...

# Symlinks horch into ~/.local/bin (or $HORCH_BIN_DIR). Never replaces a regular file.
install-cli: build
	$(OUT)/herdr-orch install-cli

# Local development: register this checkout as the plugin (link skips [[build]]).
link: build
	herdr plugin link $(CURDIR)

# End-to-end checks against an isolated herdr session.
integration: build
	./scripts/integration.sh

clean:
	rm -rf bin dist
