BINARY  := dev
PREFIX  ?= $(HOME)/.local
# Only vMAJOR.MINOR.PATCH tags are version authority; any other tag in the
# repository (a backup or rescue marker, say) must never become --version.
VERSION ?= $(shell git describe --tags --match 'v[0-9]*' --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/daviddwlee84/dev-cli/internal/cli.Version=$(VERSION)

.PHONY: build install test lint fmt vet skill-sync skill-check e2e clean all

all: fmt vet test build

build:
	go build -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/dev

# Install the binary and its agent skill together, so the documentation an
# agent reads always matches the binary that is on PATH.
install: build
	install -d $(PREFIX)/bin
	install -m 0755 $(BINARY) $(PREFIX)/bin/$(BINARY)
	$(PREFIX)/bin/$(BINARY) skill install
	@echo
	@echo 'Add to your shell rc:  eval "$$($(PREFIX)/bin/$(BINARY) shell-init zsh)"'

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

# Regenerate the skill's command reference from the live command tree.
skill-sync: build
	./$(BINARY) skill sync
	$(MAKE) build

# Fail if the checked-in command reference has drifted from the command tree.
# Wire this into CI and a pre-push hook.
skill-check: build
	./$(BINARY) skill sync --check

e2e: build
	./scripts/e2e.sh

clean:
	rm -f $(BINARY)
	go clean -testcache
