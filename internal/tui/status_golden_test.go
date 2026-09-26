package tui

import (
	"testing"
	"time"
)

// Render snapshots for the bottom chrome (Phase 1 of the kiln UI pass):
// the status line's per-mode and context/cost variants, the busy line,
// and the `/` palette. See golden_test.go for the harness these use.

func TestRenderGolden_StatusLineAsk(t *testing.T) {
	withRenderEnv(t, 80)
	s := StatusState{Mode: "manual", Cwd: "~/src/relay-api", Git: &GitStatus{Branch: "main"}, ContextWindow: 200_000}
	assertRenderGolden(t, "status-line-ask", []string{RenderStatusLine(s, 80)})
}

func TestRenderGolden_StatusLineAutoEdit(t *testing.T) {
	withRenderEnv(t, 80)
	s := StatusState{Mode: "acceptEdits", Cwd: "~/src/relay-api", Git: &GitStatus{Branch: "main", Dirty: true}, ContextWindow: 200_000, ContextUsed: intPtr(76_000)}
	assertRenderGolden(t, "status-line-auto-edit", []string{RenderStatusLine(s, 80)})
}

func TestRenderGolden_StatusLinePlan(t *testing.T) {
	withRenderEnv(t, 80)
	s := StatusState{Mode: "plan", Cwd: "~/src/relay-api", Git: &GitStatus{Branch: "main"}, ContextWindow: 200_000, Cost: 0.42}
	assertRenderGolden(t, "status-line-plan", []string{RenderStatusLine(s, 80)})
}

// TestRenderGolden_StatusLineCtx80Cost pins the context meter's high
// pressure colour (KilnRed above 70% used) alongside a nonzero cost.
func TestRenderGolden_StatusLineCtx80Cost(t *testing.T) {
	withRenderEnv(t, 80)
	s := StatusState{
		Mode: "bypassPermissions", Cwd: "~/src/relay-api", Git: &GitStatus{Branch: "main"},
		ContextWindow: 200_000, ContextUsed: intPtr(160_000), Cost: 0.42,
	}
	assertRenderGolden(t, "status-line-ctx80-cost", []string{RenderStatusLine(s, 80)})
}

func TestRenderGolden_BusyLine(t *testing.T) {
	withRenderEnv(t, 80)
	var s SpinnerState
	s.Start(2) // deterministic "Crunching" gerund
	s.SetTokens(1200)
	assertRenderGolden(t, "busy-line", s.Render(80, time.Time{}))
}

func TestRenderGolden_BusyLineThinking(t *testing.T) {
	withRenderEnv(t, 80)
	var s SpinnerState
	s.Start(0)
	s.SetThinking(true, "medium")
	assertRenderGolden(t, "busy-line-thinking", s.Render(80, time.Time{}))
}

func TestRenderGolden_PaletteSlash(t *testing.T) {
	withRenderEnv(t, 80)
	p := &Popup{Kind: KindSlashCommand, Selected: 1, Items: []AutocompleteItem{
		{Value: "model", Description: "Set the AI model"},
		{Value: "track-work", Description: "Track work as epics and stories"},
		{Value: "cost", Description: "Show cumulative session cost"},
	}}
	assertRenderGolden(t, "palette-slash", p.Render(80, 5))
}
