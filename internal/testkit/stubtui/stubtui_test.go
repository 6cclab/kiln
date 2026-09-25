package stubtui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestInitialModelZeroValue(t *testing.T) {
	m := initialModel()
	if m.committed != 0 || m.lastKey != "" || m.overlay != false {
		t.Fatalf("initialModel() = %+v, want zero value", m)
	}
	if cmd := m.Init(); cmd != nil {
		t.Fatalf("Init() = %v, want nil", cmd)
	}
}

func TestUpdateQuitsOnQ(t *testing.T) {
	m := initialModel()
	newM, cmd := m.Update(tea.KeyPressMsg{Text: "q"})
	if cmd == nil {
		t.Fatalf("Update(q) cmd = nil, want tea.Quit")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("Update(q) cmd() = %T, want tea.QuitMsg", msg)
	}
	nm := newM.(model)
	if nm.lastKey != "q" {
		t.Fatalf("Update(q) lastKey = %q, want %q", nm.lastKey, "q")
	}
}

func TestUpdatePAppendsCommittedLine(t *testing.T) {
	m := initialModel()
	newM, cmd := m.Update(tea.KeyPressMsg{Text: "p"})
	nm := newM.(model)
	if nm.committed != 1 {
		t.Fatalf("Update(p) committed = %d, want 1", nm.committed)
	}
	if cmd == nil {
		t.Fatalf("Update(p) cmd = nil, want tea.Println cmd")
	}
	msg := cmd()
	if !strings.Contains(fmt.Sprintf("%v", msg), "committed line 1") {
		t.Fatalf("Update(p) cmd() = %v, want mention of 'committed line 1'", msg)
	}

	// A second "p" continues counting from the updated model.
	newM2, _ := nm.Update(tea.KeyPressMsg{Text: "p"})
	nm2 := newM2.(model)
	if nm2.committed != 2 {
		t.Fatalf("second Update(p) committed = %d, want 2", nm2.committed)
	}
}

func TestUpdateOTogglesOverlay(t *testing.T) {
	m := initialModel()

	// Opening the overlay: no shrink command (frame grows).
	opened, cmd := m.Update(tea.KeyPressMsg{Text: "o"})
	om := opened.(model)
	if !om.overlay {
		t.Fatalf("Update(o) overlay = false, want true after opening")
	}
	if cmd != nil {
		t.Fatalf("Update(o) opening cmd = %v, want nil", cmd)
	}

	// Closing the overlay: frame shrinks.
	closed, _ := om.Update(tea.KeyPressMsg{Text: "o"})
	cm := closed.(model)
	if cm.overlay {
		t.Fatalf("Update(o) overlay = true, want false after closing")
	}
}

func TestUpdateEscClosesOverlayOnlyWhenOpen(t *testing.T) {
	m := initialModel()

	// Esc with overlay closed: no-op.
	same, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	sm := same.(model)
	if sm.overlay {
		t.Fatalf("Update(esc) with overlay closed set overlay = true")
	}
	if cmd != nil {
		t.Fatalf("Update(esc) with overlay closed cmd = %v, want nil", cmd)
	}

	// Esc with overlay open: closes it.
	m.overlay = true
	closed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	cm := closed.(model)
	if cm.overlay {
		t.Fatalf("Update(esc) with overlay open left overlay = true, want false")
	}
}

func TestViewLiveRegion(t *testing.T) {
	m := initialModel()
	m.lastKey = "x"
	m.committed = 3
	v := m.View()
	if !strings.Contains(v.Content, "key: x") {
		t.Fatalf("View() content = %q, want mention of last key", v.Content)
	}
	if !strings.Contains(v.Content, "committed: 3") {
		t.Fatalf("View() content = %q, want committed count", v.Content)
	}
	if v.Cursor == nil {
		t.Fatalf("View() cursor = nil, want set")
	}
	if v.Cursor.X != 2 || v.Cursor.Y != 0 {
		t.Fatalf("View() cursor = (%d,%d), want (2,0)", v.Cursor.X, v.Cursor.Y)
	}
}

func TestViewOverlay(t *testing.T) {
	m := initialModel()
	m.overlay = true
	v := m.View()
	lines := strings.Split(v.Content, "\n")
	if len(lines) != overlayHeight {
		t.Fatalf("View() overlay line count = %d, want %d", len(lines), overlayHeight)
	}
	if !strings.Contains(v.Content, "overlay row 0") {
		t.Fatalf("View() overlay content = %q, want overlay rows", v.Content)
	}
	if v.Cursor == nil {
		t.Fatalf("View() overlay cursor = nil, want set")
	}
	if v.Cursor.X != 0 || v.Cursor.Y != overlayHeight-1 {
		t.Fatalf("View() overlay cursor = (%d,%d), want (0,%d)", v.Cursor.X, v.Cursor.Y, overlayHeight-1)
	}
}

func TestShrinkCmdRespectsEnv(t *testing.T) {
	t.Setenv("STUBTUI_CLEAR", "")
	if cmd := shrinkCmd(); cmd != nil {
		t.Fatalf("shrinkCmd() with STUBTUI_CLEAR unset = %v, want nil", cmd)
	}

	t.Setenv("STUBTUI_CLEAR", "1")
	cmd := shrinkCmd()
	if cmd == nil {
		t.Fatalf("shrinkCmd() with STUBTUI_CLEAR=1 = nil, want tea.ClearScreen cmd")
	}
	msg := cmd()
	want := clearScreenCmd()
	if fmt.Sprintf("%T", msg) != fmt.Sprintf("%T", want) {
		t.Fatalf("shrinkCmd() cmd() = %T, want %T (tea.ClearScreen()'s message type)", msg, want)
	}
}
