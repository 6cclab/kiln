package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/claude/permission"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
)

// newTestModelWithGate builds a Model with a real *permission.Gate seeded
// to startMode, for tests that exercise cycleMode (which reads/writes the
// gate directly rather than the footer's own Mode field).
func newTestModelWithGate(startMode string) Model {
	gate := permission.NewGate(permission.GateOptions{Mode: claudesettings.PermissionMode(startMode)})
	m := NewModel(Config{
		Cwd:         "/tmp",
		ModelLabel:  "ollama/qwen3.8",
		InitialMode: startMode,
		StartedAt:   time.Unix(0, 0),
		Gate:        gate,
	})
	m.width, m.height = 100, 30
	return m
}

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

// TestView_IdleFrame checks the frame composition order while idle: the
// hint row above the editor's top rule, the editor frame, then exactly one
// mode-line row at the end — no status row (docs/claude-code-reference.md
// §1: "No status row by default. The bottom area is the input box and the
// mode line only.").
func TestView_IdleFrame(t *testing.T) {
	m := newTestModel()
	lines := viewLines(m)

	if len(lines) < 2 {
		t.Fatalf("frame too short: %d lines", len(lines))
	}
	last := lines[len(lines)-1]
	want := m.renderModeLine(m.contentWidth())
	if last != want {
		t.Errorf("last row = %q, want the mode line %q", last, want)
	}
	// Exactly one bottom row: the line above it must be the editor's own
	// bottom rule, not a second status row.
	if !isFullRule(lines[len(lines)-2]) {
		t.Errorf("row above the mode line is not the editor's bottom rule: %q", lines[len(lines)-2])
	}

	// The hint row (right-aligned effort indicator) sits directly above
	// the editor's top rule.
	topRuleIdx := -1
	for i, l := range lines {
		if isFullRule(l) {
			topRuleIdx = i
			break
		}
	}
	if topRuleIdx < 1 {
		t.Fatalf("could not find the editor's top rule (or nothing precedes it) in:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[topRuleIdx-1], "/effort") {
		t.Errorf("row above the top rule = %q, want the effort hint", lines[topRuleIdx-1])
	}

	// No spinner row while idle: the first line is the hint row, not a
	// spinner line.
	if strings.Contains(lines[0], "esc to interrupt") {
		t.Errorf("idle frame carries the busy hint: %q", lines[0])
	}
}

// TestView_SpinnerRowWhileBusy checks the spinner contributes exactly one
// row while busy and zero while idle (SpinnerState.Render's own contract).
func TestView_SpinnerRowWhileBusy(t *testing.T) {
	m := newTestModel()
	// No terminal height: bottom anchoring would otherwise absorb the
	// spinner row into the padding and keep the frame the same height.
	m.height = 0

	idleRows := len(viewLines(m))

	m.busy = true
	m.spinner.Start(0)

	busyRows := len(viewLines(m))
	if busyRows != idleRows+1 {
		t.Errorf("busy frame added %d rows, want exactly 1 (the spinner)", busyRows-idleRows)
	}

	m.busy = false
	m.spinner.Stop()
	if len(viewLines(m)) != idleRows {
		t.Errorf("frame did not return to the idle row count after Stop")
	}
}

// TestView_BottomAreaHasNoStatusRow checks the old model/context/cost
// status row is gone from the live frame: RenderStatus's own segments
// (e.g. "ctx)", the git branch marker "⎇") never appear anywhere in a
// freshly idle frame, even though FooterState/RenderStatus themselves
// still exist for whatever still constructs a StatusState directly.
func TestView_BottomAreaHasNoStatusRow(t *testing.T) {
	m := newTestModel()
	g := GitStatus{Branch: "main"}
	m.footer.Apply(StatusPatch{Git: &g})
	joined := strings.Join(viewLines(m), "\n")
	if strings.Contains(joined, "ctx)") {
		t.Errorf("frame still draws the context-window segment:\n%s", joined)
	}
	if strings.Contains(joined, "⎇") {
		t.Errorf("frame still draws the git-branch segment:\n%s", joined)
	}
}

// TestModeLine_ExactTextPerMode checks every mode's exact rendered text
// against docs/kiln-design.md's status-line contract: a "●" lead-in,
// coloured by mode (ask/manual dim, the auto-edit family green, plan
// blue), followed by the same wording the mode-line always carried.
func TestModeLine_ExactTextPerMode(t *testing.T) {
	cases := []struct {
		mode string
		want string
	}{
		{"auto", "● auto mode on (shift+tab to cycle) · ← for agents"},
		{"manual", "● manual mode on · ← for agents"},
		{"acceptEdits", "● accept edits on (shift+tab to cycle)"},
		{"plan", "● plan mode on (shift+tab to cycle)"},
		{"bypassPermissions", "● bypass permissions on (shift+tab to cycle)"},
		{"dontAsk", "● don't ask on (shift+tab to cycle)"},
	}
	for _, c := range cases {
		m := newTestModel()
		mode := c.mode
		m.footer.Apply(StatusPatch{Mode: &mode})
		// Text in the input does not affect the suffix (turn-edit.txt row
		// 40 carries it with "commit thisd function" typed).
		m.editor.SetValue("x")
		got := ansiStrip(m.renderModeLine(200))
		want := "  " + c.want
		if got != want {
			t.Errorf("mode %q: renderModeLine = %q, want %q", c.mode, got, want)
		}
	}
}

// TestModeLine_ForAgentsSuffixDroppedWhilePopupOpen checks the reference
// rule for " · ← for agents": auto and manual carry it (mode-cycle.txt),
// and it disappears while the autocomplete popup is open
// (autocomplete-slash.txt row 36).
func TestModeLine_ForAgentsSuffixDroppedWhilePopupOpen(t *testing.T) {
	m := newTestModelWithRegistry()
	mode := "auto"
	m.footer.Apply(StatusPatch{Mode: &mode})

	if got := ansiStrip(m.renderModeLine(200)); !strings.HasSuffix(got, "for agents") {
		t.Errorf("idle: renderModeLine = %q, want the for-agents suffix", got)
	}

	for _, r := range "/mod" {
		mi, _ := m.handleKey(charKey(r))
		m = mi.(Model)
	}
	if m.popup == nil {
		t.Fatal("expected a popup after typing /mod")
	}
	if got := ansiStrip(m.renderModeLine(200)); strings.HasSuffix(got, "for agents") {
		t.Errorf("popup open: renderModeLine = %q, want no for-agents suffix", got)
	}
}

// TestCtrlC_ReplacesModeLineForOneSecond drives Ctrl+C through handleKey
// (real wiring, not a synthetic Router) and checks the mode line becomes
// "Press Ctrl-C again to exit" (ctrl-c-hint.txt) until the scheduled
// msgClearModeHint arrives, then is restored.
func TestCtrlC_ReplacesModeLineForOneSecond(t *testing.T) {
	m := newTestModel()
	mode := "manual"
	m.footer.Apply(StatusPatch{Mode: &mode})

	mi, cmd := m.handleKey(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = mi.(Model)
	if got := ansiStrip(m.renderModeLine(200)); got != "  Press Ctrl-C again to exit" {
		t.Fatalf("mode line after ctrl+c = %q, want the Ctrl-C hint", got)
	}
	if cmd == nil {
		t.Fatal("ctrl+c did not return the hint-clearing Cmd")
	}
	tm := cmd()
	clear, ok := tm.(msgClearModeHint)
	if !ok {
		t.Fatalf("cmd() = %T, want msgClearModeHint", tm)
	}

	m2, _ := m.Update(clear)
	m = m2.(Model)
	if got := ansiStrip(m.renderModeLine(200)); got == "  Press Ctrl-C again to exit" {
		t.Fatalf("mode line still shows the Ctrl-C hint after msgClearModeHint: %q", got)
	}
}

// TestHintRow_CtrlYAfterKillUntilNextKeystroke checks the top hint row
// switches to "Ctrl+Y to paste deleted text" for exactly the frame right
// after a Ctrl+K/Ctrl+U kill, and reverts on the very next keystroke
// (mode-manual.txt row 8: "until the next keystroke").
func TestHintRow_CtrlYAfterKillUntilNextKeystroke(t *testing.T) {
	m := newTestModel()
	m.width, m.height = 100, 30
	m.editor.Focus()
	m.editor.SetValue("hello")

	if got := ansiStrip(m.hintRow(100)); !strings.Contains(got, "/effort") {
		t.Fatalf("hint row before any kill = %q, want the effort hint", got)
	}

	mi, _ := m.handleKey(tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	m = mi.(Model)
	if got := strings.TrimSpace(ansiStrip(m.hintRow(100))); got != "Ctrl+Y to paste deleted text" {
		t.Fatalf("hint row right after ctrl+k = %q, want the Ctrl+Y hint", got)
	}

	mi, _ = m.handleKey(charKey('!'))
	m = mi.(Model)
	if got := ansiStrip(m.hintRow(100)); strings.Contains(got, "Ctrl+Y") {
		t.Fatalf("hint row survived a keystroke after the kill: %q", got)
	}
}

// TestHintRow_OmittedWhenPromptOrPopupActive checks the task's explicit
// "When the popup or a prompt is showing, the hint row is omitted."
func TestHintRow_OmittedWhenPromptOrPopupActive(t *testing.T) {
	m := newTestModelWithRegistry()
	if got := m.hintRow(100); got == "" {
		t.Fatalf("expected a hint row with no popup/prompt active")
	}
	for _, r := range "/mod" {
		mi, _ := m.handleKey(charKey(r))
		m = mi.(Model)
	}
	if m.popup == nil {
		t.Fatal("expected a popup after typing /mod")
	}
	if got := m.hintRow(100); got != "" {
		t.Errorf("hint row with popup active = %q, want empty", got)
	}
}

// TestShiftTab_CycleOrder checks the ring app.go's cycleMode walks: auto ->
// manual -> acceptEdits -> plan -> auto, verified against
// testdata/reference/claude-code/mode-cycle.txt (SCREEN after each of 4
// shift+tab presses starting from plan mode, since plan -> auto is the
// wrap this fixture exercises).
func TestShiftTab_CycleOrder(t *testing.T) {
	m := newTestModelWithGate("plan")
	order := []string{"auto", "manual", "acceptEdits", "plan"}
	for _, want := range order {
		mi, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
		m = mi.(Model)
		if got := m.footer.State().Mode; got != want {
			t.Fatalf("after shift+tab, mode = %q, want %q", got, want)
		}
	}
}

// TestShiftTab_BypassAndDontAskCycleIntoAuto checks the task's explicit
// instruction that bypassPermissions/dontAsk are not in the ring — Shift+Tab
// from either sends the mode straight to auto.
func TestShiftTab_BypassAndDontAskCycleIntoAuto(t *testing.T) {
	for _, start := range []string{"bypassPermissions", "dontAsk"} {
		m := newTestModelWithGate(start)
		mi, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
		m = mi.(Model)
		if got := m.footer.State().Mode; got != "auto" {
			t.Errorf("shift+tab from %q = %q, want auto", start, got)
		}
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

// TestDialog_WidthInvariant checks no row a command dialog renders exceeds
// the requested width, matching the PTY driver's own per-row width
// assertion (internal/testkit/screen).
func TestDialog_WidthInvariant(t *testing.T) {
	m := newTestModel()
	m.dialog = NewCommandDialog(fakeModalSpec())
	for _, width := range []int{20, 40, 80, 117, 118, 144} {
		for _, row := range m.dialogRows(width, 0) {
			if w := VisibleWidth(row); w > width {
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

// TestApp_SlashOpensPopupAboveEditor drives the app through typing "/mod"
// one keystroke at a time (as the PTY does) and checks: the popup opens,
// the composed frame includes a row naming a matching command strictly
// above the editor's own top rule (per the task's explicit "renders
// directly above the editor's top rule" layout), and no row exceeds the
// terminal's width (the width invariant every renderer in this package
// is held to — see width.go's doc comment; rows are allowed to be
// narrower, e.g. the footer's own un-padded status text).
func TestApp_SlashOpensPopupAboveEditor(t *testing.T) {
	m := newTestModelWithRegistry()
	for _, r := range "/mod" {
		mi, _ := m.handleKey(charKey(r))
		m = mi.(Model)
	}
	if m.popup == nil {
		t.Fatal("expected a popup after typing /mod")
	}
	lines := viewLines(m)

	// Claude Code draws the suggestions directly above the input box's
	// top rule (docs/claude-code-reference.md §4, autocomplete-slash.txt),
	// so a popup row naming "model" must precede the first full-width rule.
	topRuleIdx := -1
	for i, l := range lines {
		if isFullRule(l) {
			topRuleIdx = i
			break
		}
	}
	if topRuleIdx == -1 {
		t.Fatalf("could not find the editor's top rule in the frame:\n%s", strings.Join(lines, "\n"))
	}

	found := false
	for i := 0; i < topRuleIdx; i++ {
		if strings.Contains(lines[i], "/model") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a popup row naming \"/model\" above the editor's top rule (index %d), got:\n%s", topRuleIdx, strings.Join(lines, "\n"))
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
