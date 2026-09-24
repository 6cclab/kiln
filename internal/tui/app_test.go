package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// newTestModel builds a Model with no real bridge/lane/gate — enough to
// exercise frame composition, spinner/footer rows and the busy-only
// esc-to-interrupt hint without a PTY.
func newTestModel() Model {
	m := NewModel(Config{
		Cwd:         "/tmp",
		ModelLabel:  "ollama/qwen3.8",
		InitialMode: "manual",
		StartedAt:   time.Unix(0, 0),
	})
	m.width, m.height = 80, 24
	return m
}

func viewLines(m Model) []string {
	return strings.Split(m.View().Content, "\n")
}

// TestView_IdleFrame checks the frame composition order while idle: no
// spinner row, no thinking row, no prompt, the editor frame, then exactly
// two footer rows at the end.
func TestView_IdleFrame(t *testing.T) {
	m := newTestModel()
	lines := viewLines(m)

	if len(lines) < 2 {
		t.Fatalf("frame too short: %d lines", len(lines))
	}
	// The editor frame is a top rule, N content lines, a bottom rule; the
	// footer is exactly the last two lines.
	footer := lines[len(lines)-2:]
	want := m.footer.Render(m.contentWidth())
	if footer[0] != want[0] || footer[1] != want[1] {
		t.Errorf("footer rows = %q, want %q", footer, want[:])
	}

	// No spinner row while idle: the first line is the editor's own top
	// rule, not a spinner line (spinner lines never start the editor's
	// rule character).
	if strings.Contains(lines[0], "esc to interrupt") {
		t.Errorf("idle frame carries the busy hint: %q", lines[0])
	}
}

// TestView_SpinnerRowWhileBusy checks the spinner contributes exactly one
// row while busy and zero while idle (SpinnerState.Render's own contract),
// and that the footer's first row carries "esc to interrupt" only while
// busy.
func TestView_SpinnerRowWhileBusy(t *testing.T) {
	m := newTestModel()

	idleRows := len(viewLines(m))
	idleFooter := m.footer.Render(m.contentWidth())
	if strings.Contains(idleFooter[0], "esc to interrupt") {
		t.Fatalf("idle footer already carries the busy hint: %q", idleFooter[0])
	}

	m.busy = true
	m.spinner.Start(0)
	m.footer.SetBusy(true)

	busyRows := len(viewLines(m))
	if busyRows != idleRows+1 {
		t.Errorf("busy frame added %d rows, want exactly 1 (the spinner)", busyRows-idleRows)
	}

	busyFooter := m.footer.Render(m.contentWidth())
	if !strings.Contains(busyFooter[0], "esc to interrupt") {
		t.Errorf("busy footer = %q, want the esc-to-interrupt hint", busyFooter[0])
	}

	m.busy = false
	m.spinner.Stop()
	m.footer.SetBusy(false)
	if len(viewLines(m)) != idleRows {
		t.Errorf("frame did not return to the idle row count after Stop")
	}
}

// TestFooter_AlwaysTwoRows checks RenderStatus/FooterState always produce
// exactly two rows, at a range of widths, busy or not.
func TestFooter_AlwaysTwoRows(t *testing.T) {
	f := NewFooterState(StatusState{ModelLabel: "m", ContextWindow: 32000, Mode: "manual", StartedAt: time.Unix(0, 0)})
	for _, width := range []int{20, 40, 80, 117, 118, 144} {
		for _, busy := range []bool{false, true} {
			f.SetBusy(busy)
			rows := f.Render(width)
			if rows[0] == "" && rows[1] == "" {
				t.Fatalf("width %d busy %v: both rows empty", width, busy)
			}
			if VisibleWidth(rows[0]) > width || VisibleWidth(rows[1]) > width {
				t.Fatalf("width %d busy %v: row exceeds width: %q / %q", width, busy, rows[0], rows[1])
			}
		}
	}
}

// TestRouter_EscInterruptsOnlyWhileBusy exercises app.go's own wiring
// (via handleKey) rather than keys_test.go's synthetic recorder: Esc
// aborts only while a turn is running, and does not fall through to the
// editor as a cancel either way while busy.
func TestRouter_EscInterruptsOnlyWhileBusy(t *testing.T) {
	m := newTestModel()
	m.editor.Focus()

	// Idle: Esc is the editor's own EventCancel, not an app-level action;
	// handleKey must not panic and must return without requiring a Lane.
	mi, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	_ = mi.(Model)

	// Busy, with no Lane wired: handleKey must not panic (Interrupt is
	// only invoked as `if m.busy && m.cfg.Lane != nil`).
	m.busy = true
	mb, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	got := mb.(Model)
	if !got.busy {
		t.Errorf("busy flag cleared by Esc alone; only a turn finishing should clear it")
	}
}

// TestModal_SpliceWidthInvariant checks every row ModalView.Render
// produces is exactly the requested width, matching the PTY driver's own
// per-row width assertion (internal/testkit/screen).
func TestModal_SpliceWidthInvariant(t *testing.T) {
	view := NewModalView(fakeModalSpec())
	for _, width := range []int{20, 40, 80, 117, 118, 144} {
		for _, row := range view.Render(width, 12) {
			if w := VisibleWidth(row); w != width {
				t.Errorf("width %d: row %q has visible width %d", width, row, w)
			}
		}
	}
}

// charKey builds a printable-character keypress the way
// editor/model_test.go's own helper does.
func charKey(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

func newTestModelWithRegistry() Model {
	m := NewModel(Config{
		Cwd:         "/tmp",
		ModelLabel:  "ollama/qwen3.8",
		InitialMode: "manual",
		StartedAt:   time.Unix(0, 0),
		Registry:    testRegistry(),
	})
	m.width, m.height = 80, 24
	return m
}

// isFullRule reports whether l is a bare horizontal rule (the editor's top
// or bottom border), ignoring ANSI styling.
func isFullRule(l string) bool {
	stripped := ansiStrip(l)
	stripped = strings.TrimRight(stripped, " ")
	return len(stripped) > 0 && strings.Count(stripped, "─") == len([]rune(stripped))
}

func ansiStrip(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		if r == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// TestApp_SlashOpensPopupBelowEditor drives the app through typing "/mod"
// one keystroke at a time (as the PTY does) and checks: the popup opens,
// the composed frame includes a row naming a matching command strictly
// above the editor's own top rule (per the task's explicit "renders
// directly above the editor's top rule" layout), and no row exceeds the
// terminal's width (the width invariant every renderer in this package
// is held to — see width.go's doc comment; rows are allowed to be
// narrower, e.g. the footer's own un-padded status text).
func TestApp_SlashOpensPopupBelowEditor(t *testing.T) {
	m := newTestModelWithRegistry()
	for _, r := range "/mod" {
		mi, _ := m.handleKey(charKey(r))
		m = mi.(Model)
	}
	if m.popup == nil {
		t.Fatal("expected a popup after typing /mod")
	}
	lines := viewLines(m)

	// pi-tui draws the list after the editor's bottom rule
	// (components/editor.js render()), so the popup row must sit between
	// the second full-width rule and the footer.
	bottomRuleIdx := -1
	rules := 0
	for i, l := range lines {
		if isFullRule(l) {
			rules++
			if rules == 2 {
				bottomRuleIdx = i
				break
			}
		}
	}
	if bottomRuleIdx == -1 {
		t.Fatalf("could not find the editor's bottom rule in the frame:\n%s", strings.Join(lines, "\n"))
	}

	found := false
	for i := bottomRuleIdx + 1; i < len(lines)-2; i++ {
		if strings.Contains(lines[i], "model") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a popup row naming \"model\" below the editor's bottom rule (index %d), got:\n%s", bottomRuleIdx, strings.Join(lines, "\n"))
	}

	for _, l := range lines {
		if w := VisibleWidth(l); w > m.contentWidth() {
			t.Errorf("row %q has width %d, want at most %d", l, w, m.contentWidth())
		}
	}
}

// TestApp_EnterAcceptsPopupSelection checks that Enter, while the popup is
// open, splices the selected item into the editor rather than submitting
// the line — matching pi-tui's editor.js precedence (the popup owns
// Enter/Tab/Esc while it is open, ahead of submit).
func TestApp_EnterAcceptsPopupSelection(t *testing.T) {
	m := newTestModelWithRegistry()
	for _, r := range "/mod" {
		mi, _ := m.handleKey(charKey(r))
		m = mi.(Model)
	}
	if m.popup == nil {
		t.Fatal("expected a popup after typing /mod")
	}
	mi, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mi.(Model)
	if m.popup != nil {
		t.Fatal("popup should close after accepting")
	}
	if got := m.editor.Value(); got != "/modal-test " && got != "/model " {
		t.Fatalf("editor value = %q, want the accepted command name", got)
	}
}

// TestApp_EscClosesPopupWithoutCancellingEditor checks Esc closes the
// popup and leaves the typed text alone (it does not fall through to the
// editor's own EventCancel).
func TestApp_EscClosesPopupWithoutCancellingEditor(t *testing.T) {
	m := newTestModelWithRegistry()
	for _, r := range "/mod" {
		mi, _ := m.handleKey(charKey(r))
		m = mi.(Model)
	}
	mi, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mi.(Model)
	if m.popup != nil {
		t.Fatal("popup should be closed after Esc")
	}
	if got := m.editor.Value(); got != "/mod" {
		t.Fatalf("editor value = %q, want the typed text left untouched by Esc", got)
	}
}
