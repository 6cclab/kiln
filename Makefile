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
	go test -tags e2e ./test/e2e/...

# HARNESS_E2E_LIVE=1 additionally allows tests gated on that env var (real
# model calls, real network) to run; -run Live restricts to tests named
# accordingly so a plain `make e2e` never talks to the network.
e2e-live:
	HARNESS_E2E_LIVE=1 go test -tags e2e -run Live ./test/e2e/... -timeout 10m

# The parity oracle (comparing Go and TypeScript harness output byte for
# byte on the same fixtures) lands in phase 7.
parity:
	@echo "parity oracle lands in phase 7"
	@exit 0

race:
	go test -race ./internal/testkit/...

generate:
	go generate ./...

clean:
	rm -rf $(BIN_DIR)
