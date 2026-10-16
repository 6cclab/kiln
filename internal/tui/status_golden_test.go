package tui

import (
	"strings"
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

// TestRenderGolden_StatusLineAutoNoRoute: kiln/auto before its first
// routed request renders as plain "auto" — no "→", since there is
// nothing routed yet.
func TestRenderGolden_StatusLineAutoNoRoute(t *testing.T) {
	withRenderEnv(t, 80)
	s := StatusState{Mode: "manual", ModelLabel: "auto", Cwd: "~/src/relay-api", Git: &GitStatus{Branch: "main"}, ContextWindow: 200_000}
	assertRenderGolden(t, "status-line-auto-no-route", []string{RenderStatusLine(s, 80)})
}

// TestRenderGolden_StatusLineAutoRouted: after an EventRoute, the model
// segment reads "auto → faux-2".
func TestRenderGolden_StatusLineAutoRouted(t *testing.T) {
	withRenderEnv(t, 80)
	s := StatusState{Mode: "manual", ModelLabel: "auto", RoutedModel: "faux-2", Cwd: "~/src/relay-api", Git: &GitStatus{Branch: "main"}, ContextWindow: 32_768}
	assertRenderGolden(t, "status-line-auto-routed", []string{RenderStatusLine(s, 80)})
}

// TestRenderGolden_StatusLineAutoRoutedTightCollapsesToModel: too narrow
// for "auto → faux-2" alongside the rest of the row, the model segment
// collapses to just the routed model id.
func TestRenderGolden_StatusLineAutoRoutedTightCollapsesToModel(t *testing.T) {
	withRenderEnv(t, 80)
	s := StatusState{
		Mode: "manual", ModelLabel: "auto", RoutedModel: "faux-2",
		Cwd: "~/src/a-rather-long/project/path/relay-api", Git: &GitStatus{Branch: "main"},
		ContextWindow: 32_768, Cost: 1.23, ContextUsed: intPtr(1000),
	}
	out := RenderStatusLine(s, 60)
	if VisibleWidth(stripANSI(out)) > 60 {
		t.Fatalf("overflowed: %q", out)
	}
	if plain := stripANSI(out); !strings.Contains(plain, "faux-2") || strings.Contains(plain, "auto") {
		t.Errorf("tight row = %q, want the collapsed routed model id without the virtual name", plain)
	}
	assertRenderGolden(t, "status-line-auto-routed-tight", []string{out})
}

// TestRenderGolden_StatusLineAutoRoutedPlain: plain mode writes the arrow
// as "->".
func TestRenderGolden_StatusLineAutoRoutedPlain(t *testing.T) {
	withRenderEnv(t, 80)
	SetPlainMode(true)
	defer SetPlainMode(false)
	s := StatusState{Mode: "manual", ModelLabel: "auto", RoutedModel: "faux-2", Cwd: "~/src/relay-api", Git: &GitStatus{Branch: "main"}, ContextWindow: 32_768}
	out := RenderStatusLine(s, 80)
	if !strings.Contains(out, "auto -> faux-2") {
		t.Errorf("plain-mode routed segment = %q, want \"auto -> faux-2\"", out)
	}
	assertRenderGolden(t, "status-line-auto-routed-plain", []string{out})
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
