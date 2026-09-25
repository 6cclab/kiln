package tui

import (
	"strings"
	"testing"
	"time"
)

// pi-tui's renderer throws when a component returns a line wider than the
// viewport; this Go port keeps the same discipline (see width.go's doc
// comment) even though there is no Bubbletea model in this package yet to
// crash on it. assertFits is the blanket check every render test below
// uses.
func assertFits(t *testing.T, lines []string, width int) {
	t.Helper()
	for i, line := range lines {
		if w := VisibleWidth(line); w > width {
			t.Errorf("line %d overflowed: %d > %d (%q)", i, w, width, line)
		}
	}
}

const width40 = 40

// No spaces past the scheme: a word-wrapper has nothing to break on, so
// this also covers the hard-break case rather than only the easy one.
var longURL = "https://example.com/" + strings.Repeat("a", 180)

func TestFitLinesWrapsOverflow(t *testing.T) {
	out := FitLines([]string{"> " + longURL}, width40, "")
	assertFits(t, out, width40)
}

func TestFitLinesKeepsEveryCharacter(t *testing.T) {
	out := FitLines([]string{longURL}, width40, "")
	if strings.Join(out, "") != longURL {
		t.Error("wrapping must not drop characters")
	}
}

func TestFitLinesEmitsMultipleLines(t *testing.T) {
	out := FitLines([]string{longURL}, width40, "")
	minLines := (len(longURL) + width40 - 1) / width40
	if len(out) < minLines {
		t.Errorf("got %d lines, want at least %d (guards against truncation)", len(out), minLines)
	}
}

func TestFitLinesPassthrough(t *testing.T) {
	out := FitLines([]string{"short", "also short"}, width40, "")
	if len(out) != 2 || out[0] != "short" || out[1] != "also short" {
		t.Errorf("got %v, want passthrough", out)
	}
}

func TestFitToolCallCollapsedAndExpanded(t *testing.T) {
	collapsed := RenderToolCall(ToolCallView{
		Name: "read", PrimaryArg: longURL, Status: CallOK,
		ResultLines: []string{longURL}, TotalLines: 2, HasTotalLines: true,
	})
	assertFits(t, FitLines(collapsed, width40, ""), width40)

	expanded := RenderToolCall(ToolCallView{
		Name: "read", PrimaryArg: longURL, Status: CallOK,
		ResultLines: []string{longURL, longURL},
	})
	assertFits(t, FitLines(expanded, width40, ""), width40)
}

func TestFitStatusSpinnerOneRow(t *testing.T) {
	var s SpinnerState
	s.Start(0)
	s.SetTokens(123_456)
	lines := s.Render(width40, time.Time{})
	if len(lines) != 1 {
		t.Fatalf("spinner must render exactly one line while busy, got %d", len(lines))
	}
	assertFits(t, lines, width40)
}

func TestFitStatusFooterTwoRows(t *testing.T) {
	s := StatusState{
		ModelLabel: longURL, ContextWindow: 49_152, Mode: "auto",
		StartedAt: time.UnixMilli(0), Now: time.UnixMilli(0),
		ContextUsed: intPtr(20_000),
		Git:         &GitStatus{Branch: longURL, Dirty: true},
	}
	rows := RenderStatus(s)
	lines := []string{FitStatus(rows[0], width40), FitStatus(rows[1], width40)}
	if len(lines) != 2 {
		t.Fatalf("footer must be two rows")
	}
	assertFits(t, lines, width40)
}

func TestPermissionPromptWrapsLongCommand(t *testing.T) {
	req := PermissionRequest{ToolName: "bash", PrimaryArg: "echo " + longURL, Args: map[string]any{"command": "echo " + longURL}}
	out := RenderPermissionPrompt(req, "/", width40, false, "")
	assertFits(t, out, width40)
}

func TestPlanApprovalWrapsLongLines(t *testing.T) {
	plan := "# Plan\n\n1. " + strings.Repeat("do a thing and then another thing ", 8) + "\n2. " + longURL
	out := RenderPlanApproval(plan, "", width40, 0, 0, false, "")
	assertFits(t, out, width40)
}

// TestUserMessageHasBlankRowLabelRuleThenBody checks kiln's "you" block
// anatomy (docs/kiln-design.md): one blank row, then the "you" label rule
// (amber, no meta), then the message on the raised surface. kiln drops
// the old "❯" prompt glyph from the echoed message entirely — the "you"
// label identifies the block instead.
func TestUserMessageHasBlankRowLabelRuleThenBody(t *testing.T) {
	out := RenderUserMessage("short question", width40)
	if len(out) != 3 || out[0] != "" {
		t.Fatalf("got %v, want blank/label rule/echo", out)
	}
	if !strings.Contains(out[1], "you") {
		t.Errorf("row 1 = %q, want the \"you\" label rule", out[1])
	}
	if !strings.Contains(out[2], "short question") {
		t.Errorf("got %v", out)
	}
}

func TestUserMessageHasExactlyOneLabelRule(t *testing.T) {
	msg := "short " + strings.Repeat("very long segment ", 6) + "tail"
	out := RenderUserMessage(msg, width40)
	count := 0
	for _, l := range out {
		if strings.Contains(l, "you") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("\"you\" label rule appeared %d times, want 1", count)
	}
	assertFits(t, out, width40)
}

func TestUserMessageWraps(t *testing.T) {
	out := RenderUserMessage(longURL, width40)
	if len(out) <= 1 {
		t.Error("expected wrapping into multiple lines")
	}
	assertFits(t, out, width40)
}

func TestUserMessageFindable(t *testing.T) {
	out := RenderUserMessage("find me later", width40)
	if !strings.Contains(strings.Join(out, ""), "find me later") {
		t.Error("text should remain findable")
	}
}

func TestUserMessageRewrapsOnResize(t *testing.T) {
	msg := strings.Repeat("a ", 60)
	if len(RenderUserMessage(msg, 40)) == len(RenderUserMessage(msg, 100)) {
		t.Error("rewrap at a different width should change line count")
	}
}

func TestTurnSummaryMatchesReferenceRow(t *testing.T) {
	// docs/claude-code-reference.md §3 / testdata/reference/claude-code/
	// turn-edit.txt row 21: "✻ Crunched for 4s · done 10:03 AM".
	done := time.Date(2026, 9, 24, 10, 3, 0, 0, time.UTC)
	line := strings.Join(RenderTurnSummary(TurnSummary{Seconds: 4, Verb: "Crunched", Done: done}), "")
	want := "Crunched for 4s · done 10:03 AM"
	if !strings.Contains(line, want) {
		t.Errorf("got %q, want it to contain %q", line, want)
	}
}

func TestTurnSummaryDefaultsToWorked(t *testing.T) {
	line := strings.Join(RenderTurnSummary(TurnSummary{Seconds: 1, Done: time.Unix(0, 0)}), "")
	if !strings.Contains(line, "Worked for 1s") {
		t.Errorf("got %q, want the Worked fallback", line)
	}
}
