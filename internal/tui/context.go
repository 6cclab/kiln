package tui

// The "context" block (docs/kiln-design-handoff/README.md block table,
// "context" row, and "Context segment colors"): /context's structured
// result (commands.ContextBreakdown) drawn as a header, a stacked segment
// bar, and a legend.

import (
	"fmt"
	"strings"

	"github.com/andrepato/harness/internal/commands"
)

// contextSegmentColor maps a ContextBreakdown segment's label to its kiln
// colour (docs/kiln-design-handoff/README.md "Context segment colors"):
// system prompt blue, tools violet, files read amber, conversation green,
// free the rule colour (an "empty" tone, matching the meter's own
// empty-cell colour).
func contextSegmentColor(label string) func(string) string {
	switch label {
	case "System prompt":
		return KilnBlue
	case "Tools":
		return Violet
	case "Files read":
		return KilnAmber
	case "Conversation":
		return KilnGreen
	default: // "Free"
		return Rule
	}
}

// RenderContext renders the kiln "context" block: a "context" label rule
// (dim, no meta), a header row ("<model> · Nk of Mk tokens", dim), a
// stacked bar with one run of filled cells per non-zero segment
// (proportional to width, minimum one cell, one space between runs,
// coloured per contextSegmentColor), then legend rows: a coloured "■", the
// label, the token count, and a right-aligned percentage.
func RenderContext(b commands.ContextBreakdown, width int) []string {
	lines := []string{labelRule("context", Muted, "", width)}

	usedK := formatK(b.Used)
	windowK := formatK(b.Window)
	model := b.ModelLabel
	if model == "" {
		model = "kiln"
	}
	lines = append(lines, Muted(fmt.Sprintf("%s · %s of %s tokens", model, usedK, windowK)))

	lines = append(lines, renderContextBar(b, width))

	for _, seg := range b.Segments {
		pct := 0.0
		if b.Window > 0 {
			pct = float64(seg.Tokens) / float64(b.Window) * 100
		}
		swatch := contextSegmentColor(seg.Label)(G().Segment)
		row := fmt.Sprintf("%s %s", swatch, Ink(seg.Label))
		right := fmt.Sprintf("%s %s", Muted(FormatTokens(seg.Tokens)), Muted(fmt.Sprintf("%5s", fmt.Sprintf("%.0f%%", pct))))
		pad := width - VisibleWidth(row) - VisibleWidth(right)
		if pad < 1 {
			lines = append(lines, FitStatus(row+" "+right, width))
			continue
		}
		lines = append(lines, row+strings.Repeat(" ", pad)+right)
	}
	return lines
}

// renderContextBar draws the stacked segment bar: one run of MeterFull
// cells per non-zero segment, proportional to width (minimum one cell so a
// small but present segment is never invisible), with a genuine 1-column
// gap between adjacent runs (docs/kiln-design-handoff/README.md: "1-column
// gaps" — plain, uncoloured space, not a run of its own), each run tinted
// by contextSegmentColor. Any width left over after integer rounding goes
// to the last non-zero segment, so the bar's total width always equals the
// row width the caller asked for (once the gap columns are accounted for).
func renderContextBar(b commands.ContextBreakdown, width int) string {
	if b.Window <= 0 || width <= 0 {
		return Rule(strings.Repeat(G().MeterEmpty, max(width, 0)))
	}
	var segs []commands.ContextSegment
	for _, seg := range b.Segments {
		if seg.Tokens > 0 {
			segs = append(segs, seg)
		}
	}
	if len(segs) == 0 {
		return Rule(strings.Repeat(G().MeterEmpty, width))
	}
	// One gap column between each pair of adjacent runs; the fill cells
	// share whatever width is left, so the whole row (fills + gaps) still
	// totals exactly width.
	gaps := len(segs) - 1
	barWidth := width - gaps
	if barWidth < len(segs) {
		// Too narrow to give every segment its minimum cell AND every gap
		// its column — keep the minimum-one-cell-per-segment guarantee
		// (a real but invisible segment is worse than a slightly
		// overflowing row) rather than silently dropping a segment.
		barWidth = len(segs)
	}
	type run struct {
		label string
		cells int
	}
	var runs []run
	assigned := 0
	lastIdx := -1
	for _, seg := range segs {
		cells := int(float64(seg.Tokens) / float64(b.Window) * float64(barWidth))
		if cells < 1 {
			cells = 1
		}
		runs = append(runs, run{label: seg.Label, cells: cells})
		assigned += cells
		lastIdx = len(runs) - 1
	}
	if diff := barWidth - assigned; diff != 0 && lastIdx >= 0 {
		runs[lastIdx].cells += diff
		if runs[lastIdx].cells < 1 {
			runs[lastIdx].cells = 1
		}
	}
	var b2 strings.Builder
	for i, r := range runs {
		if i > 0 {
			b2.WriteString(" ")
		}
		b2.WriteString(contextSegmentColor(r.label)(strings.Repeat(G().MeterFull, r.cells)))
	}
	return b2.String()
}

// formatK renders a token count in "Nk" form for the context header, per
// docs/kiln-design-handoff/README.md's example ("76k of 200k tokens") —
// distinct from FormatTokens, which keeps a decimal ("76.0k") and switches
// to "m" past a million; the header always wants the coarser whole-k form.
func formatK(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%dk", n/1000)
}
