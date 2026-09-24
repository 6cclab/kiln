package editor

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func newTestModel() Model {
	m := New(Styles{Marker: "❯"})
	m.Focus()
	return m
}

// key builds a plain, unmodified rune key press, e.g. key('a').
func key(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Text: string(r), Code: r}
}

func named(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "shift+enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}
	case "alt+enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "ctrl+k":
		return tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl}
	case "ctrl+u":
		return tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	case "ctrl+w":
		return tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl}
	case "ctrl+y":
		return tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl}
	case "alt+backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModAlt}
	}
	panic("unknown key: " + s)
}

func typeText(t *testing.T, m *Model, s string) {
	t.Helper()
	for _, r := range s {
		var ev Event
		*m, _, ev = m.Update(key(r))
		if ev.Kind != EventNone {
			t.Fatalf("typing %q produced unexpected event %+v", r, ev)
		}
	}
}

func press(t *testing.T, m *Model, name string) Event {
	t.Helper()
	var ev Event
	*m, _, ev = m.Update(named(name))
	return ev
}

func TestTypingAccumulatesValue(t *testing.T) {
	m := newTestModel()
	typeText(t, &m, "hello")
	if got := m.Value(); got != "hello" {
		t.Fatalf("Value() = %q, want %q", got, "hello")
	}
}

func TestEnterSubmits(t *testing.T) {
	m := newTestModel()
	typeText(t, &m, "fix the build")
	ev := press(t, &m, "enter")
	if ev.Kind != EventSubmit {
		t.Fatalf("Kind = %v, want EventSubmit", ev.Kind)
	}
	if ev.Text != "fix the build" {
		t.Fatalf("Text = %q, want %q", ev.Text, "fix the build")
	}
	if got := m.Value(); got != "" {
		t.Fatalf("buffer not cleared after submit: %q", got)
	}
}

func TestShiftEnterInsertsNewline(t *testing.T) {
	m := newTestModel()
	typeText(t, &m, "line one")
	ev := press(t, &m, "shift+enter")
	if ev.Kind != EventNone {
		t.Fatalf("shift+enter produced %+v, want EventNone", ev)
	}
	typeText(t, &m, "line two")
	if got, want := m.Value(), "line one\nline two"; got != want {
		t.Fatalf("Value() = %q, want %q", got, want)
	}
}

func TestAltEnterInsertsNewline(t *testing.T) {
	m := newTestModel()
	typeText(t, &m, "a")
	press(t, &m, "alt+enter")
	typeText(t, &m, "b")
	if got, want := m.Value(), "a\nb"; got != want {
		t.Fatalf("Value() = %q, want %q", got, want)
	}
}

func TestBackslashEnterInsertsNewlineAndDropsBackslash(t *testing.T) {
	m := newTestModel()
	typeText(t, &m, "line one\\")
	ev := press(t, &m, "enter")
	if ev.Kind != EventNone {
		t.Fatalf("backslash+enter produced %+v, want EventNone", ev)
	}
	typeText(t, &m, "line two")
	if got, want := m.Value(), "line one\nline two"; got != want {
		t.Fatalf("Value() = %q, want %q", got, want)
	}
}

func TestBackslashStateClearedByOtherKey(t *testing.T) {
	m := newTestModel()
	typeText(t, &m, "a\\")
	typeText(t, &m, "b") // any other key disarms the pending backslash
	ev := press(t, &m, "enter")
	if ev.Kind != EventSubmit {
		t.Fatalf("Kind = %v, want EventSubmit (backslash should have been disarmed)", ev.Kind)
	}
	if ev.Text != `a\b` {
		t.Fatalf("Text = %q, want %q", ev.Text, `a\b`)
	}
}

func TestPasteArrivesAsOneValueNoSubmit(t *testing.T) {
	m := newTestModel()
	var ev Event
	m, _, ev = m.Update(tea.PasteMsg{Content: "line1\nline2\nline3"})
	if ev.Kind != EventNone {
		t.Fatalf("paste produced %+v, want EventNone", ev)
	}
	if got, want := m.Value(), "line1\nline2\nline3"; got != want {
		t.Fatalf("Value() = %q, want %q", got, want)
	}
}

func TestHistoryUpDownAtEdges(t *testing.T) {
	m := newTestModel()
	m.SetHistory([]string{"first", "second", "third"})

	// Up from an empty draft recalls the newest entry first.
	press(t, &m, "up")
	if got := m.Value(); got != "third" {
		t.Fatalf("first Up = %q, want %q", got, "third")
	}
	press(t, &m, "up")
	if got := m.Value(); got != "second" {
		t.Fatalf("second Up = %q, want %q", got, "second")
	}
	press(t, &m, "up")
	if got := m.Value(); got != "first" {
		t.Fatalf("third Up = %q, want %q", got, "first")
	}
	// Up again at the oldest entry is a no-op (clamped).
	press(t, &m, "up")
	if got := m.Value(); got != "first" {
		t.Fatalf("Up past oldest = %q, want unchanged %q", got, "first")
	}

	press(t, &m, "down")
	if got := m.Value(); got != "second" {
		t.Fatalf("first Down = %q, want %q", got, "second")
	}
	press(t, &m, "down")
	if got := m.Value(); got != "third" {
		t.Fatalf("second Down = %q, want %q", got, "third")
	}
	// Down past the newest entry restores the draft, not another entry.
	press(t, &m, "down")
	if got := m.Value(); got != "" {
		t.Fatalf("Down past newest = %q, want draft %q", got, "")
	}
}

func TestHistoryPreservesDraftAtNewestSlot(t *testing.T) {
	m := newTestModel()
	m.SetHistory([]string{"old prompt"})
	typeText(t, &m, "in-progress draft")

	press(t, &m, "up")
	if got := m.Value(); got != "old prompt" {
		t.Fatalf("Up = %q, want %q", got, "old prompt")
	}
	press(t, &m, "down")
	if got := m.Value(); got != "in-progress draft" {
		t.Fatalf("Down back to draft = %q, want %q", got, "in-progress draft")
	}
}

func TestKillToLineEndAndYank(t *testing.T) {
	m := newTestModel()
	typeText(t, &m, "hello world")
	// Move cursor to just after "hello" (5 runes in).
	for i := 0; i < len("world")+1; i++ {
		var ev Event
		m, _, ev = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
		_ = ev
	}
	press(t, &m, "ctrl+k")
	if got, want := m.Value(), "hello"; got != want {
		t.Fatalf("Value() after ctrl+k = %q, want %q", got, want)
	}
	press(t, &m, "ctrl+y")
	if got, want := m.Value(), "hello world"; got != want {
		t.Fatalf("Value() after ctrl+y = %q, want %q", got, want)
	}
}

func TestKillToLineStart(t *testing.T) {
	m := newTestModel()
	typeText(t, &m, "hello world")
	for i := 0; i < len("world"); i++ {
		m, _, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	}
	press(t, &m, "ctrl+u")
	if got, want := m.Value(), "world"; got != want {
		t.Fatalf("Value() after ctrl+u = %q, want %q", got, want)
	}
	press(t, &m, "ctrl+y")
	if got, want := m.Value(), "hello world"; got != want {
		t.Fatalf("Value() after ctrl+y = %q, want %q", got, want)
	}
}

func TestKillWordBack(t *testing.T) {
	m := newTestModel()
	typeText(t, &m, "hello world")
	ev := press(t, &m, "ctrl+w")
	if ev.Kind != EventNone {
		t.Fatalf("ctrl+w produced %+v, want EventNone", ev)
	}
	if got, want := m.Value(), "hello "; got != want {
		t.Fatalf("Value() after ctrl+w = %q, want %q", got, want)
	}
	press(t, &m, "ctrl+y")
	if got, want := m.Value(), "hello world"; got != want {
		t.Fatalf("Value() after ctrl+y = %q, want %q", got, want)
	}
}

func TestAltBackspaceKillsWordBack(t *testing.T) {
	m := newTestModel()
	typeText(t, &m, "hello world")
	press(t, &m, "alt+backspace")
	if got, want := m.Value(), "hello "; got != want {
		t.Fatalf("Value() after alt+backspace = %q, want %q", got, want)
	}
}

func TestYankWithNoKillIsNoOp(t *testing.T) {
	m := newTestModel()
	typeText(t, &m, "abc")
	press(t, &m, "ctrl+y")
	if got := m.Value(); got != "abc" {
		t.Fatalf("Value() = %q, want unchanged %q", got, "abc")
	}
}

func TestPopupActiveOwnsTabEnterEsc(t *testing.T) {
	m := newTestModel()
	m.PopupActive = true
	typeText(t, &m, "/mo")

	ev := press(t, &m, "enter")
	if ev.Kind != EventNone {
		t.Fatalf("enter under popup produced %+v, want EventNone (popup owns it)", ev)
	}
	if got := m.Value(); got != "/mo" {
		t.Fatalf("Value() = %q, want unchanged %q (enter should not have submitted or edited)", got, "/mo")
	}

	ev = press(t, &m, "esc")
	if ev.Kind != EventNone {
		t.Fatalf("esc under popup produced %+v, want EventNone", ev)
	}

	ev = press(t, &m, "tab")
	if ev.Kind != EventNone {
		t.Fatalf("tab under popup produced %+v, want EventNone", ev)
	}
}

func TestEscCancelsWhenNoPopup(t *testing.T) {
	m := newTestModel()
	ev := press(t, &m, "esc")
	if ev.Kind != EventCancel {
		t.Fatalf("Kind = %v, want EventCancel", ev.Kind)
	}
}

func TestSetValueAndClear(t *testing.T) {
	m := newTestModel()
	m.SetValue("preset")
	if got := m.Value(); got != "preset" {
		t.Fatalf("Value() = %q, want %q", got, "preset")
	}
	m.Clear()
	if got := m.Value(); got != "" {
		t.Fatalf("Value() after Clear = %q, want empty", got)
	}
}

func TestScrollIndicatorAppearsBeyondMaxHeight(t *testing.T) {
	m := newTestModel()
	lines := make([]string, 12)
	for i := range lines {
		lines[i] = "line"
	}
	m.SetValue(strings.Join(lines, "\n"))
	view := strings.Join(m.View(40), "\n")
	if !strings.Contains(view, "more") {
		t.Fatalf("expected a scroll indicator in the rules, got:\n%s", view)
	}
}
