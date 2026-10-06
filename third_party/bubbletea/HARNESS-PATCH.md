# Vendored charm.land/bubbletea/v2 with patches

Source: charm.land/bubbletea/v2 v2.0.9 (the module cache copy, minus examples and
tutorials). Wired in by a `replace` directive in the root go.mod, alongside the
ultraviolet replace it depends on.

## Patch: flush a pending frame before inserting lines above it (cursed_renderer.go)

`insertAbove` (the `tea.Println` path) scrolls the terminal by the height of the last
flushed frame (`s.cellbuf.Height()`) before writing the inserted lines, then repositions.
`render` only stores the new view; it is painted on the next flush. So when one Update
both shrinks the live region and commits lines above it (a turn ending: spinner row
removed, turn summary printed), the Println message is processed with the old, taller
frame still on the glass. The terminal scrolls by that height, and when the shorter frame
is finally flushed the bottom row is left blank and the top row has scrolled away.

The patch splits `flush` into `flush` (locks) and `flushLocked`, and has `insertAbove`
call `flushLocked(false)` first whenever the pending view differs from the last flushed
one. The insert then scrolls by the correct height.

Verified through internal/testkit/screen: a one-turn faux run whose final content exactly
fills a 60x14 terminal keeps its first row on screen and ends with no trailing blank row;
before the patch it scrolled one row and left one. `make parity` fix-bug case at 100x30
matches the TypeScript oracle's rows after the patch.


## Patch: redraw the live region fully instead of an incremental diff (cursed_renderer.go)

The renderer diffs each new frame against its tracked model of the terminal and writes only
changed cells. After `insertAbove` (tea.Println) scrolls the live region — which it does for
every committed transcript line — that tracked model desyncs from the real terminal, and the
incremental diff then intermittently fails to clear a cell that changed in place (observed:
a plan-prompt / dialog selection marker that moved to another row left a stale copy on the
old row). The render call now uses `TerminalRenderer.Redraw` (full repaint of the live
region) rather than `Render` (incremental). The `viewEquals` early-return still skips
unchanged frames, so idle screens emit nothing; the live region is small, so a full repaint
per change is cheap and, under synchronized output, flicker-free.


## Patch: repaint the live region after insertAbove so the cursor lands on the caret (cursed_renderer.go)

After `insertAbove` writes the committed lines it resets its cursor model to the top of the
live region and returns, leaving the hardware cursor at the end of the committed text. The
live region has physically shifted down by the inserted-line count. Normally the next
frame's flush repaints it and repositions the cursor via `MoveTo`. But when a committed
line is the LAST thing to run — e.g. closing a dialog (which shrinks the live region) and
then committing the slash-command echo, with no view change afterward — the `viewEquals`
early-return in `flushLocked` short-circuits the next flush, so the caret is left dangling
on the committed text (observed: cursor two rows above the input box after `/mcp` Esc).

The patch, at the end of `insertAbove`, invalidates the frame cache (`s.lastView = nil`,
`s.pendingErase = true`) and calls `flushLocked(false)` once, forcing a full repaint of the
live region at its new position through the same proven path every normal frame uses. The
caret then always ends on the input box. This also removed the need for the app-level
"bottom-anchoring" pad (a full-height live region), which was masking this bug and was itself
incompatible with insertAbove (it overpainted the committed banner and left bottom residue).

Verified through internal/testkit/screen: `/mcp` open→Esc leaves the cursor on the input row
(was two rows above); a one-turn faux run leaves no trailing blank residue (OccupiedHeight
grows, never shrinks).


## Patch: flush the pending frame before the very first insertAbove too (cursed_renderer.go)

The first patch above ("flush a pending frame before inserting lines above it") only
pre-flushed when `!s.starting` — the theory being that at true startup there is nothing on
the glass yet to desync from. But `s.cellbuf` (whose `Width()`/`Height()` size every
scroll-offset computation in `insertAbove`) is only resized to the live region's actual
content height inside `flushLocked` (`frameArea.Max.Y = content.Height()` for a non-alt-
screen frame); before the renderer's first flush ever runs, `s.cellbuf` is still sized from
`newCursedRenderer`'s construction — the full terminal height, not the small live region.

kiln's startup banner is committed via `Bridge.Commit` -> `tea.Println`, off the model's own
goroutine (`internal/tui/bridge.go`'s committer). That is a genuine race against the
renderer's own fps-ticker-driven first flush: if the committed banner's `printLineMessage`
reaches `insertAbove` before that first flush has ever happened, `s.starting` is still true,
the pre-flush guard skipped it, and `h := s.cellbuf.Height()` read the full terminal height
instead of the live region's few rows. The down/up scroll math then operated over the wrong
span, inserting the banner at the wrong row and leaving a stray duplicate of its last
committed line sitting in real scrollback once the next (correctly small) flush repainted
the live region at its actual position — reproduced as a duplicate copy of the banner's tips
row appearing above the wordmark on startup, only under real scheduling (a real PTY/real
terminal), never in the synchronous, low-latency `internal/testkit/screen` driver used by
this repo's own e2e suite, which is why the goldens never caught it.

The fix drops the `!s.starting` condition entirely: `insertAbove` now always flushes a
pending view-content change first, including on the very first call. By the time
`printLineMessage`'s `insertAbove` runs, `Update` has already produced and rendered the
live region's own (small) first view (bubbletea's own event loop renders after every
processed message, before dequeuing the next one, and `Bridge.Commit`'s message is
necessarily a later message in that same loop) — so the pre-flush is never a wasted
first-frame paint, only ever the correctly-sized one `insertAbove`'s own math depends on.

## Patch: don't lose the last cell of a full-width committed line (cursed_renderer.go)

insertAbove wrote each committed line as `line + EraseLineRight + "\r\n"` with no
autowrap guard. A line exactly as wide as the terminal writes its final glyph into the
last column, which arms the terminal's pending-wrap; the subsequent EraseLineRight at the
stuck last-column cursor (and the pending-wrap state) drops that final cell. It went
unnoticed until the kiln redesign, whose label rules (`{label}────  {meta}`) are the first
content reliably drawn at exactly the terminal width — the trailing rule dash, and worse
the right-aligned filename meta (`src/math.js` clipped to `src/math.j`), disappeared.

The patch mirrors the main render path: for a line whose width is >= the terminal width,
disable autowrap (`ansi.ResetModeAutoWrap`) around the write and re-enable it after
(`ansi.SetModeAutoWrap`), and skip EraseLineRight (there is nothing to its right, and at the
last-column cursor it would erase the just-written glyph). Shorter lines keep the
write+EraseLineRight+"\r\n" path unchanged.

Verified through internal/testkit/screen: kiln label-rule rows and the "edit" block's
filename meta now render at the full terminal width with the last character intact (was
one column short); make check and make e2e green.

## Patch: keep the incremental diff on the alternate screen (cursed_renderer.go)

The full-redraw patch above starts every changed frame by clearing the screen from home.
iTerm2 archives the alternate screen into its scrollback when it is cleared, so in fullscreen
mode each state change (a keypress, a mode switch, a committed reply) left a complete stale
frame in the terminal's history: after four shift+tab presses at 120x40 the scrollback held
200 rows of old kiln frames. Changing the erase sequence (`CSI 2J` to `CSI H CSI J`) did not
help; any clear from home is archived.

The desync the full redraw works around only happens after `insertAbove`, which a fullscreen
program never calls (kiln commits into its own transcript viewport there). So the render call
keeps `Redraw` for inline mode and uses the incremental `Render` when `view.AltScreen` is set.

Verified in a real iTerm2 window (scripts/qa/drive.py, 120x40): scrollback stays at 0 rows
across two shift+tab mode changes, and the status row repaints with no residue from the
longer previous label. Test: TestCursedRenderer_altScreenFramesDoNotClear.

## Patch: recover a panic on the renderer goroutine, and a panic hook (tea.go, options.go)

The goroutine `startRenderer` starts flushes a frame on every tick and had no `recover`, unlike
the event loop and the Cmd goroutines. A panic while painting a frame ended the process with
the terminal still in raw mode, mouse reporting on and the alternate screen up. It now
recovers like a Cmd's panic (`recoverFromGoPanic`: `Run` returns `ErrProgramPanic` and the
terminal is restored) and then still receives `stopRenderer`'s stop signal, which shutdown
sends on an unbuffered channel and would otherwise wait for forever.

`WithPanicHook(fn)` is called with every panic Bubble Tea catches (value and stack) before it
prints it. kiln writes a crash report from it (`internal/crash`): Bubble Tea prints the panic
onto the alternate screen before leaving it, so the printed copy is wiped.

Tests: TestHarnessRendererPanicIsRecovered (fails without the recover: the test binary dies
with "panic: renderer broke"), TestHarnessPanicHookSeesUpdatePanic.
