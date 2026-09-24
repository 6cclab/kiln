// Package tui is the Go port of harness/src/tui/{theme,width,transcript,
// status,change-preview,permission-prompt}.ts: the pure rendering layer of
// the interactive shell, functions from state to []string with no terminal
// of their own.
//
// This package holds no Bubbletea model. Everything here is a pure function
// (theme.go, width.go, transcript.go, status.go, spinner.go, markdown.go,
// changepreview.go, permission_render.go) that takes a width and some state
// and returns lines; the model that owns the actual screen, the editor and
// keybindings is built in a later phase on top of these.
//
// # Colour and glyphs
//
// theme.go ports theme.ts: NO_COLOR/FORCE_COLOR/isatty detection, style
// helpers (dim, bold, italic, strike, the six named colours, and
// suggestion, the 256-colour approximation of Claude Code's selected-row
// colour) built on charm.land/lipgloss/v2, and the Glyphs table (Unicode by
// default, ASCII in SetPlainMode(true), mirroring --ax-screen-reader).
//
// # Width discipline
//
// width.go ports width.ts on top of github.com/charmbracelet/x/ansi
// instead of pi-tui: FitLines wraps (ansi.Wrap, leaving room for a
// continuation indent), FitStatus truncates to one row with an ellipsis
// (ansi.Truncate). Every render function in this package is responsible
// for its own width for the same reason width.ts gives: an over-wide line
// corrupts a terminal renderer's differential update, so refusing to emit
// one is not optional.
//
// # Deviations from the TypeScript source
//
// wrapTextWithAnsi (pi-tui) is replaced by ansi.Wrap, which both word-wraps
// and hard-breaks a run with no breakpoint (e.g. a bare URL); this is the
// same two-behavior contract width.ts documents, just implemented on a
// different library. truncateToWidth/visibleWidth become ansi.Truncate/
// ansi.StringWidth for the same reason.
//
// markdown.go renders through charm.land/glamour/v2 rather than pi-tui's
// Markdown component; glamour's ansi.StyleConfig is JSON-shaped (hex colour
// strings, *bool flags), not lipgloss styles, so markdownTheme's
// closure-based MarkdownTheme (app.ts:67-84) is ported as a value mapping
// instead of a function mapping. See markdown.go's doc comment for the
// specific fields that have no glamour equivalent.
package tui
