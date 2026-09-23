# Testing the TUI

Three layers. Pick the cheapest one that can actually observe the bug — and
before writing an assertion, check it can fail.

## 1. Components — `test/*.test.ts` with plain calls

Call `render(width)` and assert on the returned lines. Fast, no render loop.
Use for content, wrapping, truncation, colour.

Cannot see: anything involving more than one component, or more than one frame.

## 2. Frames — `test/support/fake-terminal.ts`

`FakeTerminal` implements pi-tui's `Terminal` against a string buffer, so the
real `TuiMainScreen` render loop runs. This is what catches width violations,
which are thrown by `doRender` and by no component:

    Error: Rendered line 3 exceeds terminal width (172 > 144)

`FakeTerminal` is a **write-log**. It records the bytes pi-tui emits. It has no
cursor, no scroll region and no cell grid, so it cannot answer "what is on
screen". Do not assert on escape sequences here to prove a layout claim — two
byte streams can differ and produce the same screen. That mistake was made once:
a test asserted `ESC[2J` instead of 37 × `ESC[2K` and passed, while the screen
was identical either way and the reported bug was untouched.

Use for: content and width across a composed frame.

## 3. Screen — `test/support/screen.ts`

Pipes `FakeTerminal`'s output into `@xterm/headless`, the emulator behind
xterm.js. Real cell grid, real cursor, real scrollback. `rows()` is what the
user is looking at; `cursorRow()` is where the next frame will start drawing,
which is what turns blanked rows into visible dead space.

Use for anything about layout over time: overlays, frames that grow and shrink,
resize, scroll.

This runs in-process. It does not exercise the shipped binary — `node-pty` would
allow that, but it does not build in this environment (`posix_spawnp failed`, no
`build/Release/*.node` after `npm rebuild`), so end-to-end pty tests are not
available and nothing here should be read as covering process startup.

## 4. Real sessions — record and replay

The layers above all simulate. When a bug is visible on a real terminal and does
not reproduce above, record the real thing instead of guessing at a repro:

    HARNESS_RECORD_TTY=/tmp/harness.rec npm run harness

Reproduce the problem, quit, then:

    npm run replay -- /tmp/harness.rec                  # final screen
    npm run replay -- /tmp/harness.rec --frames         # screen after each keystroke
    npm run replay -- /tmp/harness.rec --cols 204 --rows 52

Pass the real terminal size — layout bugs are usually size-dependent, and
replaying at the wrong width reproduces a different screen.

The output marks the cursor row, the last row with content, and the number of
blank rows below it. Dead space is `last content row` well above `cursor row`.

The recording is the exact byte stream the terminal received, plus `\x00IN:`
markers for keystrokes. It replays identically forever, without the model the
session was talking to. A bug that happens once is reproducible from then on.

`PI_TUI_DEBUG_REDRAW=1` additionally logs which render branch pi-tui took
(`first render`, `width changed`, `clearOnShrink`, …) to `pi-tui-debug.log` in
its log directory.

## Rules

- A test that cannot fail is not evidence. Reintroduce the bug and watch it go
  red before trusting a green run.
- Assert at the layer where the user perceives the bug. A layout complaint is a
  screen assertion, not a byte assertion.
- `npm run check` is typecheck plus the full suite.

## Go port

The Go port (`github.com/andrepato/harness`, this repo's `go-port` branch)
follows the same "cheapest layer that can observe the bug" principle, split
across four layers. `make check` runs the fast layers; `make e2e` and
`make e2e-live` run the slow ones. See the `Makefile` for the exact
commands each layer maps to.

### 1. Unit — `go test ./...`

Ordinary Go unit tests next to the code they cover. Fast, no subprocess, no
PTY. Use for parsing, formatting, session-store logic, hook decision logic,
tool-call plumbing — anything that doesn't need a real terminal or a real
model.

### 2. Faux — a scripted model server

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

### 3. Screen — PTY driver + `harness-drive`

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

### 4. Live e2e — `test/e2e`, gated on `HARNESS_E2E_LIVE=1`

`test/e2e` (build-tagged `e2e`, so `go build ./...`/`go test ./...`/
`make check` never pull it in) holds tests that drive the real, compiled
binaries end to end. Most of it runs against the faux server or a
scripted MCP fixture (`internal/testkit/mcpfixture`) so it's still
hermetic; `make e2e` runs that subset. Tests that actually need a real
model, real network access, or real API keys are named so they match
`-run Live` and additionally check `HARNESS_E2E_LIVE=1` before doing
anything network-bound, so a plain `make e2e` (and CI) never talks to the
network; only `make e2e-live` does.

### Record/replay via `harness-drive --record`

Like the TypeScript layer's `HARNESS_RECORD_TTY`/`npm run replay`,
`harness-drive` supports `--record <path>`: it captures the exact byte
stream the PTY produced (plus input markers for injected `SEND`/`KEY`
commands) to `<path>`, so a bug seen once on a real run can be replayed
deterministically afterward — including at a different terminal size,
which is often when layout bugs actually reproduce — without needing the
original model conversation again.
