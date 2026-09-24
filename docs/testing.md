# Testing the harness

Pick the cheapest layer that can actually observe the bug, and before writing an
assertion, check it can fail. Every screen assertion runs against an emulated
terminal, never against the bytes written: two byte streams can differ and
produce the same screen, and a write-log has no cursor, no scroll region and no
cell grid, so it cannot say what is on the glass. That distinction is where every
rendering bug in this project has lived.

`make check` runs the fast layers; `make e2e` and `make e2e-live` run the slow
ones. See the `Makefile` for the exact commands each layer maps to.

## Rules

- A test that cannot fail is not evidence. Reintroduce the bug and watch it go
  red before trusting a green run.
- Assert at the layer where the user perceives the bug. A layout complaint is a
  screen assertion, not a byte assertion.
- `make check` is the fast layers; `make e2e` drives the real binary.

## 1. Unit — `go test ./...`

Ordinary Go unit tests next to the code they cover. Fast, no subprocess, no
PTY. Use for parsing, formatting, session-store logic, hook decision logic,
tool-call plumbing — anything that doesn't need a real terminal or a real
model.

## 2. Faux — a scripted model server

`internal/testkit/faux` (owned separately from this document) runs a small
HTTP server that speaks the Anthropic and OpenAI-compatible wire formats
against a scripted transcript, so tests can drive harness against a fake
model without hitting the network or a real API key. Tests that want it
set:

- `HARNESS_FAUX_ADDR` — the faux server's listen address, so harness's
  model client points at it instead of a real provider endpoint.
- `HARNESS_FAUX_SCRIPT` — path to the script file describing the
  turn-by-turn responses (and any tool calls) the faux server should play
  back for that test.
- `HARNESS_MODEL=faux/faux-1` — selects the faux provider/model pair so
  harness's normal model-selection path is exercised unchanged, rather than
  special-cased for tests.

Use for anything that needs a real request/response round trip (streaming,
tool-call turns, retries, token accounting) without needing a real model or
real PTY.

## 3. Screen — PTY driver + `harness-drive`

`internal/testkit/screen` (owned separately) drives a real PTY running the
compiled `harness` binary and gives tests a real terminal emulator's view of
it — the Go equivalent of the TypeScript `FakeTerminal` → `@xterm/headless`
pipeline described above, but against the actual binary rather than an
in-process render loop. `cmd/harness-drive` is the CLI front end for that
driver: it launches `harness` inside a PTY and accepts a line-oriented
protocol on its own stdin to control the session and inspect the screen:

- `SEND <text>` — write text to the PTY as if typed, without a trailing key.
- `KEY <name>` — send a named key (`enter`, `esc`, `ctrl+c`, arrow keys,
  etc.) as its terminal escape sequence.
- `WAIT <pattern>` — block until the screen matches a pattern (or a
  timeout elapses), for tests that need to wait on async model/tool output
  before asserting.
- `SCREEN` — print the current screen contents (rows, cursor position) to
  the driver's own stdout, for the test to assert on.
- `RESIZE <cols> <rows>` — resize the PTY, for layout-over-time tests
  (overlays, frames that grow/shrink, scroll).
- `EXIT` — end the session and terminate the driven process.

Use for anything that must exercise process startup, real PTY resize
semantics, or real terminal escape-sequence handling — the things the
in-process TypeScript layers explicitly cannot see.

## 4. Live e2e — `test/e2e`, gated on `HARNESS_E2E_LIVE=1`

`test/e2e` (build-tagged `e2e`, so `go build ./...`/`go test ./...`/
`make check` never pull it in) holds tests that drive the real, compiled
binaries end to end. Most of it runs against the faux server or a
scripted MCP fixture (`internal/testkit/mcpfixture`) so it's still
hermetic; `make e2e` runs that subset. Tests that actually need a real
model, real network access, or real API keys are named so they match
`-run Live` and additionally check `HARNESS_E2E_LIVE=1` before doing
anything network-bound, so a plain `make e2e` (and CI) never talks to the
network; only `make e2e-live` does.

## Go TUI e2e — `test/e2e/tui_test.go`

Drives the real, compiled `harness` binary's interactive TUI through
`internal/testkit/screen`: every assertion is against the emulated
terminal grid (`Rows`/`Viewport`/`CursorRow`/`OccupiedHeight`), never
against raw escape bytes, matching the screen-layer rule above. It covers
the startup frame at several widths, the fix-bug flow, the busy/spinner
state, the inline permission prompt (allow and deny-with-feedback), the
`/model`/`/permissions`/`/mcp`/`/agents`/`/config` panels, Shift+Tab's
permission-mode cycle, the Ctrl+R transcript view, a resize sweep, `--ax-
screen-reader`, the `!`/`#` input modes, and Ctrl+C/Ctrl+D exit.

Run it as part of `make e2e`, or on its own:

    go test -tags e2e ./test/e2e/... -run TestTUI_ -v

To update a golden after an intentional UI change:

    UPDATE=1 go test -tags e2e ./test/e2e/... -run TestTUI_FixBug -v

Goldens live under `testdata/golden/tui-*.txt`. `testdata/faux/slow.yaml`
(a single response with a `delay`) exists so the busy-state test has
something to observe while a turn is still running.

A few of these tests carry comments describing real, reproduced bugs
found while writing them (a modal panel whose key-hint/status rows get
silently clipped by an undersized height budget; selecting a model
hanging the whole program) rather than routing around them — see the
comments at each call site, and the suite's own report, for the repro.

`testdata/drive/tui-smoke.txt` reproduces the fix-bug flow by hand through
`cmd/harness-drive` instead of a Go test, for a person (or another agent)
driving the real binary interactively:

    go build -o bin/harness ./cmd/harness
    go build -o bin/faux ./cmd/faux
    ./bin/faux testdata/faux/fix-bug.yaml &   # prints its listen address, e.g. 127.0.0.1:54321
    go run ./cmd/harness-drive --bin bin/harness \
      --env HARNESS_FAUX_ADDR=127.0.0.1:54321 \
      --env HARNESS_FAUX_API=anthropic-messages \
      --env HARNESS_MODEL=faux/faux-1 \
      -- --permission-mode bypassPermissions \
      < testdata/drive/tui-smoke.txt

## Record/replay via `harness-drive --record`

Like the TypeScript layer's `HARNESS_RECORD_TTY`/`npm run replay`,
`harness-drive` supports `--record <path>`: it captures the exact byte
stream the PTY produced (plus input markers for injected `SEND`/`KEY`
commands) to `<path>`, so a bug seen once on a real run can be replayed
deterministically afterward — including at a different terminal size,
which is often when layout bugs actually reproduce — without needing the
original model conversation again.
