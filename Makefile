MODULE := github.com/andrepato/harness
BIN_DIR := bin

.PHONY: build check e2e e2e-live race generate clean

# kiln-drive (internal/testkit/screen's PTY driver, cmd/kiln-drive) is a
# dev/test tool, not part of the `build` target; build it directly with:
#   go build -o $(BIN_DIR)/kiln-drive ./cmd/kiln-drive
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/andrepato/harness/internal/cli.Version=$(VERSION)

build:
	mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/kiln ./cmd/kiln

check:
	@echo "==> gofmt"
	@fmtout="$$(gofmt -l .)"; \
	if [ -n "$$fmtout" ]; then \
		echo "gofmt found unformatted files:"; \
		echo "$$fmtout"; \
		exit 1; \
	fi
	@echo "==> go vet"
	go vet ./...
	@echo "==> staticcheck"
	@if [ -x "$$HOME/go/bin/staticcheck" ]; then \
		"$$HOME/go/bin/staticcheck" ./...; \
	else \
		go run honnef.co/go/tools/cmd/staticcheck@latest ./...; \
	fi
	@echo "==> go test"
	go test ./...

# test/e2e is gated behind the e2e build tag so `go build ./...` and
# `make check` never pull it in; only `make e2e`/`make e2e-live` do.
e2e:
	go test -tags e2e ./test/e2e/... -count=1 -timeout 10m

# HARNESS_E2E_LIVE=1 additionally allows tests gated on that env var (real
# model calls, real network) to run; -run Live restricts to tests named
# accordingly so a plain `make e2e` never talks to the network.
e2e-live:
	HARNESS_E2E_LIVE=1 go test -tags e2e -run Live ./test/e2e/... -timeout 10m

race:
	go test -race ./internal/testkit/...

generate:
	go generate ./...

clean:
	rm -rf $(BIN_DIR)
