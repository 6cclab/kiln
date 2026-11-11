package tui

import (
	"image/color"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// sameColour compares two color.Color values by their RGBA, so a lipgloss
// hex string and whatever the cursor carries compare equal regardless of the
// concrete type lipgloss hands back.
func sameColour(a, b color.Color) bool {
	if a == nil || b == nil {
		return a == b
	}
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}

// caretColour is the colour the editor paints its caret with — set from the
// marker style by neutralTextareaStyles, so it tracks whichever editor.Styles
// refreshEditorStyles last pushed (amber normally, violet in bash mode).
func caretColour(t *testing.T, m Model) color.Color {
	t.Helper()
	c := m.editor.Cursor()
	if c == nil {
		t.Fatalf("editor reported no cursor")
	}
	return c.Color
}

// TestBashEditorStylesViolet pins the pure builder: bash mode recolours the
// marker, caret and rules to the violet tools accent while the normal theme
// uses amber. Without the recolour the input box gives no signal that a "!"
// line runs as a shell command.
func TestBashEditorStylesViolet(t *testing.T) {
	marker := G().UserMark
	normal := editorStyles(marker)
	bash := bashEditorStyles(marker)

	violet := lipgloss.Color(CurrentTextHex().Violet)
	if got := bash.MarkerStyle.GetForeground(); !sameColour(got, violet) {
		t.Errorf("bash marker = %v, want violet %v", got, violet)
	}
	if got := bash.Rule.GetForeground(); !sameColour(got, violet) {
		t.Errorf("bash rule = %v, want violet %v", got, violet)
	}
	if sameColour(normal.MarkerStyle.GetForeground(), violet) {
		t.Errorf("normal marker is already violet (%v); the modes are indistinguishable", normal.MarkerStyle.GetForeground())
	}
}

// TestBashModeInputTogglesStyling is the regression for "kiln input should
// change colors when running ! bash script": typing a leading "!" turns the
// input box violet, and clearing it (or submitting) returns it to amber. It
// drives real keystrokes through handleKey so it also covers the transition
// tracking in refreshEditorStyles.
func TestBashModeInputTogglesStyling(t *testing.T) {
	violet := lipgloss.Color(CurrentTextHex().Violet)
	amber := lipgloss.Color(CurrentTextHex().Amber)

	m := newTestModel()
	if m.bashInput {
		t.Fatalf("fresh model already in bash mode")
	}
	if got := caretColour(t, m); !sameColour(got, amber) {
		t.Fatalf("idle caret = %v, want amber %v", got, amber)
	}

	// Typing "!" enters bash mode: box turns violet.
	mi, _ := m.handleKey(charKey('!'))
	m = mi.(Model)
	if !m.bashInput {
		t.Fatalf("after typing %q the input is not in bash mode", "!")
	}
	if got := caretColour(t, m); !sameColour(got, violet) {
		t.Errorf("bash caret = %v, want violet %v", got, violet)
	}

	// A second character keeps bash mode (and must not re-churn into amber).
	mi, _ = m.handleKey(charKey('l'))
	m = mi.(Model)
	if !m.bashInput {
		t.Fatalf("bash mode dropped after a second character")
	}
	if got := caretColour(t, m); !sameColour(got, violet) {
		t.Errorf("bash caret after second char = %v, want violet %v", got, violet)
	}

	// Backspacing both characters empties the line: back to amber.
	for range 2 {
		mi, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
		m = mi.(Model)
	}
	if m.editor.Value() != "" {
		t.Fatalf("editor not empty after backspaces: %q", m.editor.Value())
	}
	if m.bashInput {
		t.Fatalf("still in bash mode with an empty line")
	}
	if got := caretColour(t, m); !sameColour(got, amber) {
		t.Errorf("caret after clearing = %v, want amber %v", got, amber)
	}
}

// TestBashModeExitsOnSubmit checks that submitting a "!" line (which resets
// the editor) returns the box to amber, so the next prompt is not stuck
// violet on an empty line.
func TestBashModeExitsOnSubmit(t *testing.T) {
	violet := lipgloss.Color(CurrentTextHex().Violet)
	amber := lipgloss.Color(CurrentTextHex().Amber)

	m := newTestModel()
	for _, r := range "!ls" {
		mi, _ := m.handleKey(charKey(r))
		m = mi.(Model)
	}
	if !m.bashInput {
		t.Fatalf("typing %q did not enter bash mode", "!ls")
	}
	if got := caretColour(t, m); !sameColour(got, violet) {
		t.Fatalf("caret before submit = %v, want violet %v", got, violet)
	}

	mi, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mi.(Model)
	if m.bashInput {
		t.Fatalf("still in bash mode after submitting the line")
	}
	if got := caretColour(t, m); !sameColour(got, amber) {
		t.Errorf("caret after submit = %v, want amber %v", got, amber)
	}
}
