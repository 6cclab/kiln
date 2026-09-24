// Package editor is the Go port of Claude Code's input line — the
// BorderedEditor wrapper in src/tui/app.ts (lines 121-215) plus the editor
// setup in runApp (app.ts:584-601) — built on charm.land/bubbles/v2's
// textarea instead of pi-tui's Editor component.
//
// It is deliberately self-contained: it takes its colours and glyphs as a
// small injected Styles value (styles.go) rather than importing the
// package-root tui package's theme, and does its own ANSI width
// arithmetic directly on github.com/charmbracelet/x/ansi. Both choices
// exist so this package can be built and tested before theme.go/width.go
// (owned elsewhere in internal/tui) exist.
//
// # What bubbles/v2's textarea already provided
//
// The wrapped textarea.Model supplies: the character buffer, multi-line
// editing (insert, backspace, delete, word-left/right movement), its own
// viewport with vertical scrolling once content exceeds its height,
// line-wrapping to a configured width, Ctrl+K/Ctrl+U/Ctrl+W/Alt+Backspace
// key bindings that already do the deletions this package's kill ring
// needs (deleteAfterCursor, deleteBeforeCursor, deleteWordLeft), a hardware
// cursor via Cursor() (once SetVirtualCursor(false) turns off its own
// inline block), tea.PasteMsg handling that inserts pasted text as one
// atomic edit, and per-focus-state styling via Styles/SetStyles.
//
// # What this package built on top
//
//   - Enter-submits-not-inserts: textarea's own KeyMap binds Enter to
//     InsertNewline, so Model.Update intercepts "enter" itself before any
//     message reaches the textarea, rather than reconfiguring that
//     binding — the same message would otherwise need two different
//     behaviours depending on state (popup open, backslash pending), which
//     a static key binding can't express.
//   - The `\` + Enter and Shift+Enter/Alt+Enter newline escapes
//     (model.go's pendingBackslash) — pi-tui's editor.js has an equivalent
//     (shouldSubmitOnBackslashEnter), but only when Shift+Enter is also
//     bound to submit, a case that does not apply here since Shift/Alt+
//     Enter are dedicated newline keys in this port; the state machine is
//     simplified accordingly, per the task's instruction.
//   - Prompt history recalled at the buffer's first/last line
//     (navigateHistory in model.go), a straight port of pi-tui's
//     navigateHistory (editor.js) onto this package's own history slice —
//     bubbles/v2 has no history concept at all.
//   - The kill ring (killring.go): Ctrl+K/Ctrl+U/Ctrl+W/Alt+Backspace
//     capture the text a forwarded key is about to delete (mirroring
//     textarea's own deleteWordLeft rune-for-rune for the Ctrl+W case, so
//     the captured text matches exactly what gets removed) and Ctrl+Y
//     yanks the most recent one back in. textarea has no kill ring or
//     yank at all.
//   - The BorderedEditor frame itself (view.go): the two full-width rules,
//     the `Marker + " "` prefix on every content line, the placeholder
//     splice for an empty buffer, and a scroll hint drawn into the rules
//     when the textarea has more lines than fit its capped height.
//
// # What textarea has no equivalent for
//
// Undo/redo: pi-tui's editor.js has an UndoStack (Ctrl+Z / Ctrl+Shift+Z,
// coalescing consecutive typing into one undo step). bubbles/v2's textarea
// (v2.2.1) has no undo stack, no Ctrl+Z binding, and no exported hook this
// package could use to build one without reaching into textarea's private
// line-buffer representation. This is an absence, not a design choice —
// documented here per the task's instruction to record it rather than
// silently drop it. A future pass could add undo by snapshotting
// Model.Value()+cursor before each edit, the same coarse approach
// SetValue-based recall already uses for history.
//
// # Deviations from app.ts worth naming explicitly
//
//   - No inline cursor block. app.ts's placeholder splice inserts the dim
//     text *after* pi-tui's own inline cursor cell, because pi-tui's
//     Editor renders its cursor as a styled character within the line
//     itself. This package uses the hardware cursor
//     (SetVirtualCursor(false), Model.Cursor) instead, per the task's
//     instruction, so there is no inline cell to splice after — the
//     placeholder simply starts at column 0, where that cell would have
//     been.
//   - The marker's column accounting differs by one space from
//     BorderedEditor's, because pi-tui's Editor supplies a leading
//     paddingX space that bubbles/v2's textarea has no equivalent for.
//     innerWidth's doc comment (view.go) has the detail; the rendered
//     frame is the same shape either way.
package editor
