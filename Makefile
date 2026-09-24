MODULE := github.com/andrepato/harness
BIN_DIR := bin

.PHONY: build check e2e e2e-live parity race generate clean

# harness-drive (internal/testkit/screen's PTY driver) is owned by another
# agent and doesn't have a main.go yet. Once cmd/harness-drive/main.go
# lands, add:
#   go build -o $(BIN_DIR)/harness-drive ./cmd/harness-drive
# to this target.
build:
	mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/harness ./cmd/harness

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

# The parity oracle: runs the same scripted faux fixture through the real
# TS harness (test/parity/run-ts.mts, in-process against @xterm/headless)
# and the real Go harness (bin/harness, through a real PTY via
# internal/testkit/screen) and diffs what lands on screen. See
# test/parity/parity_test.go's header for how each side is driven.
#
# node_modules is not installed in this worktree; symlink it from the main
# checkout first: ln -s /path/to/harness/node_modules node_modules
parity: build
	go test -tags parity ./test/parity/... -count=1 -timeout 5m -v

race:
	go test -race ./internal/testkit/...

generate:
	go generate ./...

clean:
	rm -rf $(BIN_DIR)
