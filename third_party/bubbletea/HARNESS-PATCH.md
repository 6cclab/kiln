# Vendored charm.land/bubbletea/v2 with one patch

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
