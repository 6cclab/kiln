# Main CI repair after PR 68

## Scope

PR 68 merged at `64f834b` with two red CI gates, and the next main run
(`43220371085`, on `e5b0442`) failed three:

- `check`: Linux staticcheck reported `U1000` for `enableWorkerShell`
  (`internal/agent/worker_shell.go:15`).
- `cover`: `internal/agent` at 85.91% (1268/1476), below its unchanged 86.90% floor.
- `race`: `TestRunWorkerBatchAutomaticIntegration/disjoint` detected a data race
  in `subagentEventSink` (`internal/cli/chat.go`).

Isolated worker Bash stays refused by dispatch on every platform, because
detached descendants cannot yet be owned safely. Nothing here enables it.

## Change

- `enableWorkerShell` is deleted. Its only caller was the macOS live Seatbelt test,
  which now calls `enableWorkerShellForOS(..., runtime.GOOS)`. That function still
  rejects Linux, Windows, and a claimed macOS target on a non-macOS host before a
  shell can start.
- `subagentEventSink` serialises its stderr writes. Concurrent workers report
  `Dispatcher.OnEvent` events from their own goroutines, so unsynchronised
  `Fprintf` calls could interleave or corrupt lines. The TUI sinks only call
  `Bridge.Send` and are unaffected.
- New agent tests cover durable worker-batch replay decisions, refusal on failure
  and cancellation, batch preconditions, isolated file confinement at tool
  execution, and worker Bash with no sandbox. Each checks the safe operation (a
  confined write inside the worker root) as well as the rejected paths.

## Evidence

- Lint: built with `go install honnef.co/go/tools/cmd/staticcheck@2026.2.2` and run
  as `GOOS=linux staticcheck ./internal/agent/...`. It reports the `U1000` without
  the change and passes with it.
- Race: `TestSubagentEventSinkSerialisesConcurrentWorkers` fails without the mutex
  ("wrote to stderr from two goroutines at once") and passes with it, without
  needing `-race`. `go test -race -count=3 -run
  'TestRunWorkerBatchAutomaticIntegration|TestSubagentEventSink' ./internal/cli`
  passes.
- Coverage: a disposable `golang:1.26.6` linux/arm64 container measured
  `internal/agent` at 87.1%. CI runs linux/amd64, so the next CI run confirms the
  exact figure.
- `TestWorkerShellActivationCannotSpoofDarwin` (non-macOS only) passes in the same
  container. With the `runtime.GOOS != "darwin"` guard mutated away, it fails with
  `isolated worker shell requires confined files and a worker job scope` instead of
  the macOS Seatbelt refusal.

## Provenance

The original `feat/efficient-orchestration-memory` checkout was checkpointed
unchanged in `a9f1eae` (parent `5a7150f`; draft PR 71). Its 22 source files were
already byte-identical to readiness ancestor `966fb18`, so main has newer
reviewed versions of all of them. Its two documents are on main: the AGENTS.md
shared-cache rule (#70) and `scripts/orca/efficiency-coordinator.md` (this change).
The crash-test temp-dir cleanup (`098a7cf`) merged separately as #69.
