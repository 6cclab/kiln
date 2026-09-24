# Vendored github.com/charmbracelet/ultraviolet with one patch

Source: github.com/charmbracelet/ultraviolet@v0.0.0-20260811164956-006e29f97886, the version
charm.land/bubbletea/v2 v2.0.9 pins. Wired in by a `replace` directive in the root go.mod.

## Patch: erase against the old model before resizing (terminal_renderer.go, Render)

In inline (non-alternate-screen) mode, when a frame is shorter than the previous one and a
full clear is pending, `Render` resized `s.curbuf` to the new height before `clearUpdate`
ran. `clearBelow` then called `move`, which clamps the tracked cursor row to
`curbuf.Height()-1`, so the cursor-up distance was computed from the new height instead of
the old one. The terminal received `CursorUp(new-1)` and `EraseScreenBelow`, which left the
taller frame's top rows on screen and drew the new frame below them.

The patch runs `clearUpdate` first on the clear path and resizes afterwards. The diff path
(no pending clear) still resizes before diffing, because the diff loop walks the new rows.

Verified through internal/testkit/screen against the stub TUI: closing a 12-row overlay
over a 3-row live region returns `OccupiedHeight` to its pre-open value, and lines committed
with `tea.Println` stay on screen. The unpatched renderer left nine stale rows.

Upstream as of 2026-09-22 (v0.0.0-20260922123528-4e49372c11f9) still has the early resize.
The full diff is in HARNESS-PATCH.diff. Re-vendor when upstream fixes it, then drop the
replace directive.
