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
	return append([]string{labelRule("system", Muted, "", width)}, noteBody(text, width)...)
}

// noteBody is a note's text, wrapped to the width rather than clipped: a
// note is often the one line saying what just happened.
func noteBody(text string, width int) []string {
	var out []string
	for _, line := range wrapMultiline(text, width) {
		out = append(out, Muted(line))
	}
	return out
}

// CommitNote commits a system note block (a blank row, the note, then a
// trailing blank row so the next block's own leading blank keeps blocks
// separated by exactly one row, matching every other Commit call site in
// this file). Not margin-padded here: CommitSynthetic stores these lines
// for a later replay to splice back in unpadded, and calls Commit, which
// applies the left margin once, centrally (see its own doc comment in
// bridge.go).
//
// A note straight after another note continues that block rather than
// opening a second "system" rule: "✕ Declined …" then "■ Interrupted …"
// read as one account of what happened.
func (b *Bridge) CommitNote(text string) {
	b.FreezeBefore()
	width := ruleWidth()
	b.mu.Lock()
	follows := b.lastWasNote
	b.mu.Unlock()
	lines := append([]string{""}, RenderNote(text, width)...)
	if follows {
		lines = noteBody(text, width)
	}
	b.CommitSynthetic(lines)
	b.mu.Lock()
	b.lastWasNote = true
	b.mu.Unlock()
}
