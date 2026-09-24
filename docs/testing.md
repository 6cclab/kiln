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

## 5. ccparity — held against the real Claude Code

`test/e2e/ccparity_test.go` (+ `ccparity_normalize.go`) drives the real harness
TUI through `internal/testkit/screen`, exactly as `tui_test.go` does, and
compares the resulting screen against a reference screen captured from the
real `claude` binary under `testdata/reference/claude-code/` (see that
directory's README and `docs/claude-code-reference.md`, the rendering
contract these goldens back). Run it with:

    make ccparity
    # or directly:
    go test -tags e2e -run TestCCParity ./test/e2e/... -count=1 -v

This suite is expected to be red while `internal/tui` is under active
rewrite (three parallel efforts as of 2026-09-24: S1 shell, S2 transcript, S3
dialogs). Its job is to make every remaining difference from Claude Code
visible and precise, not to pass on any given day. As of this writing
`cmd/harness` itself does not build (`internal/tui/app.go` references
`TranscriptView`/`msgAltScreenAppend`/etc. that don't exist yet on this
branch mid-edit), so the suite currently fails at `TestMain`'s build step,
before any subtest runs — a stronger failure than a red assertion. Rerun
`make ccparity` once `cmd/harness` builds again to see the real per-scenario
diffs.

### The normaliser

Both the reference row and the harness's own row are passed through
`normalizeScreen` (`ccparity_normalize.go`) before comparison, so a scenario
fails on a real difference, not on machine-specific or non-deterministic
noise:

- Trailing spaces stripped.
- `v2.1.281` / any `vX.Y[.Z]` version string → `vX`.
- The harness's own `<provider>/<model>` display (`faux/faux-1`,
  `ollama/llama3`, …) and Claude Code's model display (`Opus 5 (1M
  context)`, `claude-opus-5`) → `MODEL`.
- Each side's cwd (the harness's actual scratch project dir, passed in by
  the caller, and the fixed capture-time path every reference screen was
  taken under, `.../var/folders/.../T/cc-ref/proj`) → `CWD`.
- Clock times (`10:03 AM`) → `HH:MM XM`.
- `for <N>s` (turn-summary durations) → `for Ns`.
- Token-count fractions (`47k/1.0m`, `0/1.0m`) → `N tokens`.
- Dollar costs (`$0.53`) → `$N`; percentages (`6%`) → `N%`.
- Rows that are Claude Code product content, out of this project's scope
  (per `docs/claude-code-reference.md`'s "Out of scope" note), are dropped
  entirely rather than normalised: `  ⎿  Tip:` rows, the `▎ ※ Claim` banner,
  `Get to finished work sooner…`, the `⚠ N MCP server(s)` warning row, and
  any row containing this machine's own `statusLine` output (`│ ⎇ `).

Deliberately **not** normalised: indentation, column positions, glyphs,
wording, row order — a scenario's whole point is to catch a difference in
those.

A failure prints a unified diff (`github.com/aymanbagabas/go-udiff`) of the
normalised rows, plus a "columns differ" note for any row pair that's
identical once leading whitespace is stripped, so an indentation bug reads
apart from a wording/element bug.

### Styles

`internal/testkit/screen`'s `Screen.Styles()` reads the x/vt emulator's cell
buffer (fg, bg, bold, dim, italic, underline, reverse per cell) and
`screen.EncodeStyledRow`/`EncodeStyledScreen` render it as a compact golden
encoding: runs of cells sharing one style become one span,
`[fg=#d97757 b]text[/]`; an unstyled span is written as plain text with no
brackets. Token vocabulary: `fg=#rrggbb`, `bg=#rrggbb`, `b` bold, `d`
dim/faint, `i` italic, `u` underline, `r` reverse; `[`, `]`, `\` in text are
escaped as `\[`, `\]`, `\\`. `Screen.GoldenStyles(t, name)` compares against
`testdata/golden/<name>.styles.txt` (`UPDATE=1` to write). `harness-drive`
exposes the same encoding over the line protocol as `SCREEN --styles`.

`cmd/vtreplay` replays a `.rec` recording (see "Record/replay" below; a NUL
byte followed by `IN:` marks a keystroke, and — a parsing assumption, not a
documented format guarantee — the marker's text always ends at the next ESC
(`0x1b`) byte, because every observed PTY response opens with one) through
the same VT emulator, sized at `--cols`/`--rows`, and prints the final
screen in the same styles encoding; `--frames` also prints the screen
immediately before each `\x00IN:` marker is applied. The reference styles
goldens under `testdata/reference/claude-code/*.styles.txt` were produced
this way from `manual-session.rec` and `bash-plan2-session.rec`:

    go run ./cmd/vtreplay --cols 100 --rows 40 --frames \
      testdata/reference/claude-code/manual-session.rec

### Colour table (observed, `manual-session.rec` + `bash-plan2-session.rec`)

| Element | Colour |
| --- | --- |
| Logo block (`▐▛███▛█`/`▝▜██████▀`/`▝▝ ▝▝`) | fg `#d77757` (brand orange); the solid mid-glyphs additionally carry bg `#000000` |
| `❯` (prompt marker, dialog selection arrow) | fg `#b1b9f9` |
| Rule `─` (input box) | fg `#888888` |
| Rule `▔` (dialog top rule) | fg `#b1b9f9` |
| Rule `╌` (diff / plan section divider) | fg `#505050` |
| `⏺` on a tool-call header (`Update(math.js)`) | fg `#4eba65` (green) |
| `⏺` on a plain assistant line (`Done.`) | fg `#ffffff` |
| `⎿` result marker | fg `#999999` |
| Diff `-` row | prefix (` 1 -`) fg `#dc5a5a` bg `#3d0100`; row text fg `#f8f8f2` bg `#3d0100`; the specific removed token additionally bg `#5c0200` |
| Diff `+` row | prefix (` 1 +`) fg `#50c850` bg `#022800`; row text syntax-highlighted on bg `#022800` (keyword `#f92672`, function name `#a6e22e`, params `#fd971f`, `function` keyword `#66d9ef`, default text `#f8f8f2`); the specific added token additionally bg `#044700` |
| Selected dialog row | `❯` fg `#b1b9f9`; the selected option's label fg `#4eba65` with a `✔` also fg `#4eba65` |
| Dim description text | fg `#999999` |
| Turn summary `✻` | fg `#999999` |
| Mode line, auto | `⏵⏵ auto mode on` fg `#ffc107` (amber), the rest of the line fg `#999999` |
| Spinner glyph | **not captured** — every reference frame available was taken mid-settle, never mid-spin; `[chk]` |
| `⚠` warning row | **not captured** — only present in `startup-default-home.txt`, which has no matching `.rec` (colour data doesn't exist for it); `[chk]` |

### Known gaps (not done)

- `resize-60.txt` is named in `docs/claude-code-reference.md` but does not
  exist under `testdata/reference/claude-code/`; `TestCCParity` has no
  RESIZE scenario because there's nothing to compare against. Needs a
  capture.
- `dialog-rewind.txt` has a scenario (`TestCCParity_RewindDialog`) but no
  matching `.rec`, so it has no styles golden and its exact keystroke
  sequence (esc-esc after which turn) wasn't independently confirmed
  against a capture script the way the other scenarios were against
  `capture-manual.txt`/`capture-bash-plan2.txt` — treat its row-level
  comparison as unverified until a `.rec` exists.
- `turn-bash-auto.txt` likewise has no matching `.rec` in this checkout (its
  wording — `echo parity-check` — doesn't appear in either available
  recording), so `TestCCParity_BashTurn` compares text only, no styles.
- The harness has no `--permission-mode auto`; `TestCCParity_BashTurn` and
  the plan-mode scenario use `bypassPermissions`/`plan` as the closest
  available modes, per the task's own fallback instruction. Whether that's
  actually equivalent to Claude Code's auto mode for these screens is
  unverified.
- No scenario was actually run to completion against a working `harness`
  binary — see the build-break note above. Every scenario here compiled
  (`go vet -tags e2e ./test/e2e/...`) and was reasoned through against the
  reference `.txt` files' exact keystroke sequence, but none has a real
  pass/fail result yet.

## Record/replay via `harness-drive --record`

Like the TypeScript layer's `HARNESS_RECORD_TTY`/`npm run replay`,
`harness-drive` supports `--record <path>`: it captures the exact byte
stream the PTY produced (plus input markers for injected `SEND`/`KEY`
commands) to `<path>`, so a bug seen once on a real run can be replayed
deterministically afterward — including at a different terminal size,
which is often when layout bugs actually reproduce — without needing the
original model conversation again.
