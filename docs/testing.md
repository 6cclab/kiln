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
