package tui

// The "kiln" block, live streaming variant (docs/kiln-design-handoff/
// README.md's "Streaming" section): while the assistant's text is still
// arriving, the live region shows the last few wrapped rows of what has
// streamed so far, with a trailing amber caret, instead of the plain
// markdown-rendered block a completed message commits as.

// maxStreamRows is how many wrapped rows of in-flight text the live region
// shows (docs/kiln-design-handoff/README.md: "last 8 lines").
const maxStreamRows = 8

// RenderStreamLive renders the live "kiln" block: a muted `kiln ───` label
// rule, the last maxRows rows of text wrapped plainly to width (no markdown
// rendering — glamour's cost per keystroke-sized delta would make every
// stream tick expensive; the committed block re-renders the full markdown
// once streaming ends, in msgCommitMarkdown), with the streaming caret
// (G().StreamCaret, kiln amber) appended to the last row.
func RenderStreamLive(text string, width, maxRows int) []string {
	rule := labelRule("kiln", Muted, "", width)
	if text == "" {
		return []string{rule, KilnAmber(G().StreamCaret)}
	}
	// FitLines only wraps a line that overflows width as one call; text
	// here may already contain embedded newlines (paragraph breaks in the
	// streamed markdown source), so wrap each source line independently
	// and flatten, same as ansiWrap's own per-line contract.
	rows := wrapMultiline(text, width)
	if len(rows) > maxRows {
		rows = rows[len(rows)-maxRows:]
	}
	if len(rows) == 0 {
		rows = []string{""}
	}
	last := rows[len(rows)-1]
	caret := KilnAmber(G().StreamCaret)
	if VisibleWidth(last)+VisibleWidth(G().StreamCaret) > width {
		// Fit the caret onto the row rather than overflow it: truncate the
		// text one cell short and append the caret in its place.
		last = FitStatus(last, width-VisibleWidth(G().StreamCaret))
	}
	rows[len(rows)-1] = last + caret
	return append([]string{rule}, rows...)
}

// wrapMultiline splits text on "\n" and wraps each resulting line to width
// independently via FitLines, flattening the result — FitLines itself only
// wraps a single already-newline-free line per call.
func wrapMultiline(text string, width int) []string {
	var out []string
	for _, line := range splitLines(text) {
		out = append(out, FitLines([]string{line}, width, "")...)
	}
	return out
}
