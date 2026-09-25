package editor

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// markerColumns is the number of columns View reserves for the marker
// glyph plus the one space after it (`Marker + " "`, no second space —
// see innerWidth's doc comment for why the TS source's own accounting
// differs from this by that one space).
func (m Model) markerColumns() int {
	return ansi.StringWidth(m.styles.Marker) + 1
}

// innerWidth is the width handed to the wrapped textarea: the frame width
// minus the marker-and-space prefix every content line gets in View.
//
// app.ts's BorderedEditor renders the textarea only one column narrower
// than the frame, not two, because pi-tui's Editor already reserves a
// leading space of its own (`paddingX: 1`) that Claude Code's marker sits
// in front of — see app.ts:157-168's comment on why there is "no space
// after the marker" there. bubbles/v2's textarea has no such paddingX, so
// this package reserves the marker's own space explicitly instead of
// relying on one the wrapped widget doesn't supply. The frame is
// identical either way; only which side owns the gap differs.
func (m Model) innerWidth(width int) int {
	inner := width - m.markerColumns()
	if inner < 1 {
		inner = 1
	}
	return inner
}

// View renders the BorderedEditor frame: a full-width top rule, the
// textarea's content lines each prefixed with the marker, and a full-width
// bottom rule — ported from BorderedEditor.render (app.ts:157-179). Every
// returned line is exactly width columns wide by ansi.StringWidth.
func (m Model) View(width int) []string {
	if width < 1 {
		width = 1
	}
	inner := m.innerWidth(width)

	ta := m.ta // value type: SetWidth here does not mutate the receiver
	ta.SetWidth(inner)

	above, below := m.scrollHints(&ta)
	body := strings.Split(ta.View(), "\n")

	prefix := m.styles.MarkerStyle.Render(m.styles.Marker) + " "
	// continuation is the two-space indent every row after the first gets —
	// whether it is a wrapped continuation of one long logical line or a
	// second logical line the user entered with Shift+Enter. Only the very
	// first rendered row carries the marker, matching RenderUserMessage's
	// own convention for the same text once it is echoed into the
	// transcript (transcript.go, resultIndent) and
	// docs/claude-code-reference.md §2's resize-60.txt citation (`❯ Use the
	// Edit tool …` then `  Do not explain.`, no repeated marker). It happens
	// to be the same width as the marker prefix (markerColumns), since a
	// single-cell marker plus its trailing space is two columns.
	continuation := strings.Repeat(" ", m.markerColumns())
	empty := ta.Value() == ""

	out := make([]string, 0, len(body)+2)
	out = append(out, m.rule(width, above))
	for i, line := range body {
		if empty && i == 0 {
			line = m.splicePlaceholder(line, inner)
		} else {
			line = fitWidth(line, inner)
		}
		if i == 0 {
			out = append(out, prefix+line)
		} else {
			out = append(out, continuation+line)
		}
	}
	out = append(out, m.rule(width, below))
	return out
}

// rule draws one full-width horizontal rule, with an optional centred
// scroll hint spliced in — pi-tui's "─── ↑ 3 more ───" (app.ts's comment on
// fullWidthRule), ported here as the same shape rather than the identical
// string, since pi-tui's own glyph choice is not specified beyond that
// comment.
func (m Model) rule(width int, hint string) string {
	if hint == "" {
		return m.styles.Rule.Render(strings.Repeat("─", width))
	}
	label := " " + hint + " "
	remaining := width - ansi.StringWidth(label)
	if remaining < 0 {
		return m.styles.Rule.Render(strings.Repeat("─", width))
	}
	left := remaining / 2
	right := remaining - left
	return m.styles.Rule.Render(strings.Repeat("─", left) + label + strings.Repeat("─", right))
}

// scrollHints reports how many lines are scrolled out of view above and
// below the textarea's current viewport, for rule() to draw. LineCount
// counts logical (newline-delimited) lines rather than wrapped visual
// rows, so this under-counts a single very long wrapped line the same way
// pi-tui's own indicator does — both are an approximation of "how much
// more is there", not an exact row count.
func (m Model) scrollHints(ta interface {
	LineCount() int
	Height() int
	ScrollYOffset() int
},
) (above, below string) {
	total := ta.LineCount()
	height := ta.Height()
	offset := ta.ScrollYOffset()
	if total <= height {
		return "", ""
	}
	if offset > 0 {
		above = fmt.Sprintf("↑ %d more", offset)
	}
	if remaining := total - height - offset; remaining > 0 {
		below = fmt.Sprintf("↓ %d more", remaining)
	}
	return above, below
}

// splicePlaceholder splices the dim placeholder into an empty input's sole
// content line, right after the marker-and-space prefix View has already
// added — ported from BorderedEditor.placeholderLine (app.ts:208-214), with
// one deviation named in doc.go: because this package uses the hardware
// cursor (SetVirtualCursor(false)) rather than pi-tui's inline cursor
// block, there is no inline cell to splice after, so the placeholder starts
// immediately, giving `❯ Try "how do I log an error?"` — one space after
// the marker, matching startup-default-home.txt row 9 — instead of the
// extra blank cell an earlier pass here left in place for the inline-cursor
// layout pi-tui uses but this package does not.
func (m Model) splicePlaceholder(line string, inner int) string {
	trimmed := strings.TrimRight(line, " ")
	room := inner - ansi.StringWidth(trimmed)
	if room < 0 {
		room = 0
	}
	text := []rune(DefaultPlaceholder)
	if room < len(text) {
		text = text[:room]
	}
	withPlaceholder := trimmed + m.styles.Placeholder.Render(string(text))
	pad := inner - ansi.StringWidth(withPlaceholder)
	if pad < 0 {
		pad = 0
	}
	return withPlaceholder + strings.Repeat(" ", pad)
}

// fitWidth defensively normalizes a rendered content line to exactly want
// columns. In practice bubbles/v2's textarea already pads/truncates every
// line to its configured width (verified against v2.2.1's source), so this
// is a belt-and-suspenders check for the width invariant the render tests
// assert on, not a routine code path.
func fitWidth(line string, want int) string {
	w := ansi.StringWidth(line)
	if w == want {
		return line
	}
	if w > want {
		return ansi.Truncate(line, want, "")
	}
	return line + strings.Repeat(" ", want-w)
}
