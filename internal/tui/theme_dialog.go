package tui

import "charm.land/lipgloss/v2"

// Dialog-specific colours, additive to theme.go (owned by another agent in
// this work item; this file is separate so it can land without touching
// theme.go).
//
// Evidence and its limits: colours below rgb(80,200,120) were read by
// finding the byte offset of the target glyph in
// testdata/reference/claude-code/manual-session.rec and taking the
// nearest preceding `\x1b[38;2;r;g;bm` SGR sequence within a 300-byte
// lookback window. That is NOT a full terminal-state trace (no proper
// ANSI parser walking the whole buffer applying/clearing SGR state cell
// by cell), so it can pick up an SGR meant for adjacent, unrelated text if
// the lookback window crosses a paint boundary. Treat every value here as
// [chk] — a lead, not a confirmed reading — until re-verified with a real
// cell-by-cell SGR walk (or `harness-drive`'s planned `SCREEN --styles`
// dump, per docs/claude-code-reference.md section 8).
//
// Where the value already exists in theme.go under a name whose own
// comment documents real evidence (Muted, Suggestion, CallGreen, CallRed),
// this file reuses that token rather than re-deriving it, on the
// assumption (unverified for dialogs specifically) that Claude Code reuses
// one colour system across the transcript and its dialogs.
var (
	// DialogAccent is the selected dialog row's `❯` marker and bolded
	// label. Reused from Suggestion (rgb(177,185,249)), theme.go's
	// documented accent for a selected row elsewhere in the UI. Not
	// independently re-verified for dialog rows specifically — [chk].
	DialogAccent = Suggestion

	// DialogCheck is the `✔` after a dialog's current value. Read off the
	// nearest preceding SGR before the first `✔` in manual-session.rec
	// (byte offset ~1940): rgb(80,200,120). This is close to but distinct
	// from theme.go's CallGreen (rgb(78,186,101)); kept as its own token
	// rather than assumed equal to CallGreen, since the two readings
	// disagree and I did not resolve why. [chk].
	DialogCheck = style(lipgloss.NewStyle().Foreground(lipgloss.Color("#50C878")))

	// DialogFail reuses CallRed (rgb(255,107,128)) for `✘` in /mcp
	// server rows — not independently re-verified against a capture that
	// contains a failed MCP server; dialog-mcp.txt (plain text, no
	// colour) shows the glyph's position but not its SGR. [chk].
	DialogFail = CallRed

	// DialogWarn reuses Amber (rgb(255,193,7)) for `⚠` ("needs
	// authentication") rows in /mcp. Not independently re-verified for
	// this specific glyph in a dialog context. [chk].
	DialogWarn = Amber

	// DialogTitle is the "Edit file" / "Ready to code" style heading text
	// inside the edit-permission and plan-approval prompts: read off the
	// SGR immediately preceding "Edit file" in manual-session.rec (byte
	// offset 13000: rgb(177,185,249) then bold) and immediately preceding
	// "Ready to code" in bash-plan2-session.rec (byte offset 39966: the
	// same rgb(177,185,249) then bold). Same rgb as Suggestion/
	// DialogAccent, applied bold here — real evidence, two independent
	// captures agree, so this one is NOT [chk].
	DialogTitle = style(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#B1B9F9")))

	// DialogRule is the `╌` separator inside the edit-permission prompt:
	// read off the SGR immediately preceding the first `╌` in
	// manual-session.rec (byte offset 13070): rgb(80,80,80). This is
	// darker than theme.go's RuleColour (rgb(136,136,136)) — real
	// evidence, not a reuse guess, but read from one capture only, so the
	// plan-approval `─`/`╌` rules are assumed (not independently checked)
	// to share it. [chk] for the plan-approval reuse specifically.
	DialogRule = style(lipgloss.NewStyle().Foreground(lipgloss.Color("#505050")))
)
