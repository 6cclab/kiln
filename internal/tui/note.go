package tui

// The "note" block (docs/kiln-design-handoff/README.md block table,
// "note" row): a single dim line under a "system" label rule, used for
// interrupt/decline/reconnect notices and for a slash command's result
// when that result is a single line ("/cost", "/compact", "/model",
// "/agents", "/help" — a multi-line result keeps CommitCommandResult's
// "⎿ " form instead, since a note has no room for continuation rows).

// RenderNote renders the kiln "system" block: a "system" label rule (dim,
// no meta) above one dim line, fitted to width so it truncates rather than
// wraps (a note is meant to read as a single aside, docs/kiln-design-
// handoff/README.md: "One dim line, e.g. …").
func RenderNote(text string, width int) []string {
	return []string{
		labelRule("system", Muted, "", width),
		FitStatus(Muted(text), width),
	}
}

// CommitNote commits a system note block (a blank row, the note, then a
// trailing blank row so the next block's own leading blank keeps blocks
// separated by exactly one row, matching every other Commit call site in
// this file).
func (b *Bridge) CommitNote(text string) {
	b.FreezeBefore()
	width := ruleWidth()
	lines := append([]string{""}, RenderNote(text, width)...)
	b.CommitSynthetic(lines)
}
