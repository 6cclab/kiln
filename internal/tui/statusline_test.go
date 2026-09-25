package tui

import (
	"strings"
	"testing"
	"time"
)

// TestStatusLine_RunsCommandAndRenders drives the configured statusLine
// command through the model the way the event loop would (refreshStatusLine
// returns a tea.Cmd; its message is applied by Update) and checks the row is
// rendered below the input box, above the mode line, indented two spaces —
// matching Claude Code's "│ ⎇ …" row (docs/claude-code-reference.md §1).
func TestStatusLine_RunsCommandAndRenders(t *testing.T) {
	m := NewModel(Config{
		Cwd:               "/tmp",
		ModelLabel:        "faux/faux-1",
		InitialMode:       "manual",
		StartedAt:         time.Unix(0, 0),
		StatusLineCommand: `printf 'MODEL | branch main | ready'`,
	})
	m.width, m.height = 100, 24

	cmd := m.refreshStatusLine()
	if cmd == nil {
		t.Fatal("refreshStatusLine returned nil with a command configured")
	}
	msg := cmd() // runs the command synchronously off the model
	sl, ok := msg.(msgStatusLine)
	if !ok {
		t.Fatalf("refreshStatusLine message = %T, want msgStatusLine", msg)
	}
	mi, _ := m.Update(sl)
	m = mi.(Model)

	lines := viewLines(m)
	var statusIdx, modeIdx = -1, -1
	for i, l := range lines {
		if strings.Contains(l, "branch main | ready") {
			statusIdx = i
		}
		if strings.Contains(ansiStrip(l), "manual mode on") {
			modeIdx = i
		}
	}
	if statusIdx == -1 {
		t.Fatalf("statusLine row not rendered:\n%s", strings.Join(lines, "\n"))
	}
	if modeIdx == -1 || statusIdx >= modeIdx {
		t.Errorf("statusLine (row %d) must sit above the mode line (row %d)", statusIdx, modeIdx)
	}
	if !strings.HasPrefix(lines[statusIdx], "  ") {
		t.Errorf("statusLine row not indented two spaces: %q", lines[statusIdx])
	}
}

// TestStatusLine_NoCommandNoRow: with no command configured, no statusLine
// row and no refresh command.
func TestStatusLine_NoCommandNoRow(t *testing.T) {
	m := NewModel(Config{Cwd: "/tmp", ModelLabel: "faux/faux-1", InitialMode: "manual", StartedAt: time.Unix(0, 0)})
	m.width, m.height = 100, 24
	if cmd := m.refreshStatusLine(); cmd != nil {
		t.Error("refreshStatusLine returned a command with no statusLine configured")
	}
	for _, l := range viewLines(m) {
		if strings.Contains(l, "branch") {
			t.Errorf("unexpected statusLine row with no command: %q", l)
		}
	}
}
