MODULE := github.com/andrepato/harness
BIN_DIR := bin

.PHONY: build check e2e e2e-live qa qa-lint eval eval-live cover covercheck race race-full generate clean

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
	@echo "==> go vet (e2e build tag)"
	go vet -tags e2e ./test/e2e/...

# test/e2e is gated behind the e2e build tag so `go build ./...` and
# `make check` never pull it in; only `make e2e`/`make e2e-live` do.
# `check` still vets the e2e-tagged package (see above) so a compile break
# there fails the fast layer instead of only surfacing in the slower `e2e`
# job.
e2e:
	go test -tags e2e ./test/e2e/... -count=1 -timeout 10m

# HARNESS_E2E_LIVE=1 additionally allows tests gated on that env var (real
# model calls, real network) to run; -run Live restricts to tests named
# accordingly so a plain `make e2e` never talks to the network.
e2e-live:
	HARNESS_E2E_LIVE=1 go test -tags e2e -run Live ./test/e2e/... -timeout 10m

# Real-terminal QA (macOS only): drives bin/kiln through qa/scenarios in a
# real terminal window via Orca and records screenshots under qa/runs.
# SCENARIO=<area>/<name> runs one scenario (default: all of them);
# TERMINAL is iterm-dark, iterm-light, terminal or warp. See docs/testing.md.
TERMINAL ?= iterm-dark
SCENARIO ?=
QA_STEPS := $(if $(SCENARIO),qa/scenarios/$(SCENARIO).steps,$(wildcard qa/scenarios/*/*.steps))
qa: build
	go build -o $(BIN_DIR)/faux ./cmd/faux
	python3 scripts/qa/drive.py --terminal $(TERMINAL) --out qa/runs/$(shell date +%Y%m%dT%H%M%S) $(QA_STEPS)

qa-lint:
	python3 scripts/qa/drive.py --lint $(QA_STEPS)

# kiln eval: runs every eval/scenarios entry against the faux provider
# only (no network, no credentials), then reports the result against
# eval/results/baseline.jsonl, failing if any (scenario, model) pair's
# score dropped by more than 0.1. EVAL_REPEAT (default 1) is passed through
# to `eval run --repeat`; the nightly workflow sets it to 3 for flake/variance
# detection, a plain `make eval` stays at 1.
EVAL_REPEAT ?= 1
eval: build
	$(BIN_DIR)/kiln eval run --results eval/results -j 4 --repeat $(EVAL_REPEAT)
	$(BIN_DIR)/kiln eval report --results eval/results --fail-on-regression 0.1

# HARNESS_EVAL_LIVE gates real model calls the same way HARNESS_E2E_LIVE
# gates e2e: `make eval` never talks to the network, `make eval-live` does,
# against MODELS (a comma-separated provider/model list) opted in
# explicitly. REPEAT repeats each (scenario, model) pair to see variance.
MODELS ?= faux/faux-1
REPEAT ?= 1
eval-live: build
	KILN_EVAL_LIVE=1 $(BIN_DIR)/kiln eval run --models $(MODELS) --repeat $(REPEAT) --results eval/results

# cover: full coverage run for the whole module. -covermode=atomic is
# required for the coverage numbers to be race-detector-safe if this profile
# is ever combined with a -race run; here it's just the mode go tool cover
# expects. Prints the module-wide total (last line of `go tool cover -func`)
# followed by the per-package table from cmd/covercheck (see there for the
# aggregation/format details).
cover:
	go test -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out | tail -1
	go run ./cmd/covercheck -profile coverage.out -report

# covercheck: cover, then fail if any package listed in .coverage-floors
# has dropped below its floor. Packages not listed are reported but never
# fail the build (see .coverage-floors for how floors were set and how to
# add a new package to the list).
covercheck: cover
	go run ./cmd/covercheck -profile coverage.out -floors .coverage-floors

# race: the fast race layer, run in CI on every push/PR. -short skips the
# one test known to take minutes under -race: internal/tools'
# TestSessionSearchToolResultFormat, whose sqlite FTS sync indexes the real
# default session roots (~/.harness/sessions, ~/.claude/projects) in
# addition to its own fixtures, so this stays fast; race-full runs
# everything, including that test, and is reserved for the nightly workflow.
race:
	go test -race -short ./...

race-full:
	go test -race ./...

generate:
	go generate ./...

clean:
	rm -rf $(BIN_DIR)
