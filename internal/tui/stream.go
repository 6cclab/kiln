package tui

// The "kiln" block, live streaming variant (docs/kiln-design-handoff/
// README.md's "Streaming" section): while the assistant's text is still
// arriving, the live region shows the rows of what has streamed so far,
// rendered as the markdown the completed message commits as (streammd.go),
// with a trailing amber caret.

// maxStreamRows is how many wrapped rows of in-flight text the live region
// shows (docs/kiln-design-handoff/README.md: "last 8 lines").
const maxStreamRows = 8

// RenderStreamLive renders the live "kiln" block: a muted `kiln ───` label
// rule, then the last maxRows rows of text rendered as markdown at width,
// holding back what is still ambiguous (streammd.go's settle), with the
// streaming caret (G().StreamCaret, kiln amber) appended to the last row.
func RenderStreamLive(text string, width, maxRows int) []string {
	return renderStreamLiveRows(text, width, maxRows, nil)
}

// renderStreamLiveRows is RenderStreamLive, keeping the finished blocks'
// rows in cache between frames when it is not nil: the same rows, without
// rendering the whole reply on every frame.
func renderStreamLiveRows(text string, width, maxRows int, cache *streamMarkdown) []string {
	rule := labelRule("kiln", Muted, "", width)
	if text == "" {
		return []string{rule, KilnAmber(G().StreamCaret)}
	}
	if cache == nil {
		cache = &streamMarkdown{}
	}
	rows := cache.tailRows(text, width, maxRows)
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
