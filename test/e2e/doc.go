//go:build e2e

// Package e2e holds harness's end-to-end tests: tests that drive the real
// built binaries (harness, harness-drive) rather than calling internal
// packages directly.
//
// It is gated behind the "e2e" build tag so that `go build ./...`,
// `go test ./...` and `make check` never compile or run it; only
// `make e2e` (go test -tags e2e ./test/e2e/...) and `make e2e-live`
// (the same, plus HARNESS_E2E_LIVE=1 and -run Live, for tests that make
// real model calls) do.
//
// See docs/testing.md for the full description of harness's Go test
// layers.
package e2e
