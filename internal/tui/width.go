package tui

import "github.com/charmbracelet/x/ansi"

// Keeping rendered lines inside the viewport.
//
// Ported from width.ts. There, pi-tui throws when a component returns a
// line wider than the terminal — an over-wide line corrupts its
// differential rendering, so the alternative to an error is a garbled
// screen. That constraint carries over even though this Go port's eventual
// Bubbletea model does not necessarily crash on an over-wide line the way
// pi-tui does: every component here is still responsible for its own
// width, because most of what this harness renders is content it did not
// author (a pasted URL, a path in a tool call, a diff hunk, a model-written
// plan, a line the user typed).
//
// Two behaviors, same split as the TS original:
//
//   - FitLines wraps. Use it for content — losing the end of a URL or a
//     diff line is worse than taking an extra row.
//   - FitStatus truncates. Use it for single-row indicators like the
//     spinner and footer, where wrapping would shove the layout around to
//     say something the user can already infer.

// VisibleWidth is the ANSI-aware display width of a string, matching
// pi-tui's visibleWidth.
func VisibleWidth(s string) int {
	return ansi.StringWidth(s)
}

// ansiWrap wraps a single line at limit, preserving ANSI escapes and
// breaking a run with no breakpoint (e.g. a bare URL) rather than
// overflowing — the same two-behavior contract pi-tui's wrapTextWithAnsi
// gives width.ts.
func ansiWrap(line string, limit int) string {
	return ansi.Wrap(line, limit, "")
}

// FitLines wraps any line that would overflow width, preserving every
// character. Lines that already fit pass through unchanged. A continuation
// indent keeps wrapped output legible inside an already indented block (a
// prompt, a plan), but it has to leave room for itself or the wrap just
// overflows again one level down.
func FitLines(lines []string, width int, indent string) []string {
	if width <= 0 {
		return []string{}
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if VisibleWidth(line) <= width {
			out = append(out, line)
			continue
		}
		room := width - VisibleWidth(indent)
		if room < 1 {
			room = 1
		}
		wrapped := ansiWrap(line, room)
		first := true
		for _, part := range splitLines(wrapped) {
			if first {
				out = append(out, part)
				first = false
				continue
			}
			out = append(out, indent+part)
		}
	}
	return out
}

// splitLines splits on "\n" the way ansi.Wrap emits its rows, without
// pulling in strings.Split for a one-line helper that must not be confused
// with splitting caller content on other boundaries.
func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

// FitStatus truncates a line to a single row, with an ellipsis when
// something was cut.
func FitStatus(line string, width int) string {
	if width <= 0 {
		return ""
	}
	if VisibleWidth(line) <= width {
		return line
	}
	return ansi.Truncate(line, width, "…")
}
