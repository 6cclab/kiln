package tui

// The "plan" block (docs/kiln-design-handoff/README.md block table, "plan"
// row): kiln's live todo checklist, updated in place while the turn is in
// progress and committed once, in its final state, at the end of the turn.
// Owned by this file (moved out of transcript.go, which used to render it
// as "Update Todos" — the kiln label rule now carries the "plan" identity
// instead, matching every other block).

import (
	"fmt"
)

// RenderPlan renders the kiln "plan" block: a label rule ("plan", dim,
// meta "{done}/{n}"), a "Plan · d of n" header row (dim), then one row per
// item — done items green-checked with dim strikethrough text, the
// in-progress item amber with ink text, and a pending item a faint circle
// with muted text, so "which one is happening now" is answerable at a
// glance (docs/kiln-design-handoff/README.md, "plan" row).
func RenderPlan(items []TodoView, width int) []string {
	gl := G()
	done := 0
	for _, it := range items {
		if it.Status == TodoCompletedStatus {
			done++
		}
	}
	lines := []string{
		labelRule("plan", Muted, fmt.Sprintf("%d/%d", done, len(items)), width),
		Muted(fmt.Sprintf("Plan · %d of %d", done, len(items))),
	}
	// Two spaces between glyph and text, not one: the design puts the glyph
	// in its own 2ch box plus a 10px gap (Terminal.dc.html:88), which at
	// 14px Fira Code (~8.4px/col) reads as glyph + 2 columns before the
	// text starts (finding plan-glyph-spacing) — not glyph + 1 this used
	// to render. Subagent rows (subagents.go) use a distinct grid-column
	// layout instead of a glyph+gap pair (design's "agents" block, line 95,
	// is `grid-template-columns` with its own 14px gaps, not a 2ch glyph
	// box), so this convention doesn't carry over there — no change needed
	// on that side.
	for _, it := range items {
		switch it.Status {
		case TodoCompletedStatus:
			lines = append(lines, fmt.Sprintf("%s  %s", KilnGreen(gl.OK), Muted(Strike(it.Content))))
		case TodoInProgressStatus:
			lines = append(lines, fmt.Sprintf("%s  %s", KilnAmber(gl.PlanCurrent), Ink(it.Content)))
		default:
			lines = append(lines, fmt.Sprintf("%s  %s", Faint(gl.PlanTodo), Muted(it.Content)))
		}
	}
	return lines
}
