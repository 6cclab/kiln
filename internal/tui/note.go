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
	return append([]string{labelRule("system", Muted, "", width)}, noteBody(text, width, Muted)...)
}

// noteBody is a note's text, wrapped to the width rather than clipped: a
// note is often the one line saying what just happened.
func noteBody(text string, width int, colour func(string) string) []string {
	var out []string
	for _, line := range wrapMultiline(text, width) {
		out = append(out, colour(line))
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
	b.commitNote(text, Muted)
}

// CommitMistakeNote is CommitNote for a note telling the user they used
// something wrong (an unknown command, a missing argument): red text,
// same block.
func (b *Bridge) CommitMistakeNote(text string) {
	b.commitNote(text, KilnRed)
}

func (b *Bridge) commitNote(text string, colour func(string) string) {
	b.FreezeBefore()
	width := ruleWidth()
	b.mu.Lock()
	follows := b.lastWasNote
	b.mu.Unlock()
	// follows is fixed at commit time (whether the note straight before
	// this one in the transcript was itself a note, continuing one
	// "system" block rather than opening a second) and captured into the
	// rebuild closure so a later resize redraws the same shape — only the
	// width changes, never whether the rule itself is present
	// (qa/findings/…-system-blocks-not-reflowed-on-resize.json: the label
	// rule, and the body it wraps, must redraw at the new width like every
	// other block instead of keeping their old one indefinitely).
	rebuild := func(w int) []string {
		if follows {
			return noteBody(text, w, colour)
		}
		return append([]string{"", labelRule("system", Muted, "", w)}, noteBody(text, w, colour)...)
	}
	b.CommitSyntheticRebuild(rebuild(width), rebuild)
	b.mu.Lock()
	b.lastWasNote = true
	b.mu.Unlock()
}
