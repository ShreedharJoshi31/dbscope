# dbscope: build the web UI, then embed it in one Go binary.
# Needs Go 1.26+ (a newer toolchain downloads automatically) and Node 22.12+.

PNPM    ?= npx -y pnpm@12.15.0
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/dbscope/dbscope/internal/agent.Version=$(VERSION)
BIN     ?= bin/dbscope

.PHONY: build web go test test-go test-web e2e dev clean

build: web go

web:
	$(PNPM) --dir web install --frozen-lockfile
	$(PNPM) --dir web build

# CGO is off so the same command works on macOS, Linux and Windows.
go:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/dbscope

test: test-go test-web

# Database tests start PostgreSQL containers through Docker, or use
# DBSCOPE_TEST_DSN; without either they are skipped. Each engine's package
# starts its own servers (several JVMs among them), so packages run two at a
# time: all at once, they exhausted a 4 GB Docker VM and timed out.
test-go:
	go vet ./...
	go test -count=1 -p 2 -timeout 30m ./...

test-web:
	$(PNPM) --dir web install --frozen-lockfile
	$(PNPM) --dir web lint
	$(PNPM) --dir web typecheck
	$(PNPM) --dir web test

e2e:
	$(PNPM) --dir web exec playwright install chromium
	$(PNPM) --dir web e2e

# Two terminals: `make dev` serves the UI with hot reload on :5173 against an
# agent started with `dbscope ui --dev-origin http://localhost:5173`.
dev:
	$(PNPM) --dir web dev

clean:
	rm -rf bin web/dist/assets web/dist/index.html
