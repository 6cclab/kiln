package tui

import (
	"fmt"
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
// editor frame, then exactly one status-line row at the end
// (docs/kiln-design-handoff/README.md "Screen anatomy": the bottom area is
// the input box and the one-row status line only — no hint row above it
// any more).
func TestView_IdleFrame(t *testing.T) {
	m := newTestModel()
	lines := viewLines(m)

	if len(lines) < 2 {
		t.Fatalf("frame too short: %d lines", len(lines))
	}
	last := lines[len(lines)-1]
	want := m.renderStatusRow(m.contentWidth())
	if last != want {
		t.Errorf("last row = %q, want the status row %q", last, want)
	}
	// Exactly one bottom row: the line above it must be the editor's own
	// bottom rule, not a second status row.
	if !isFullRule(lines[len(lines)-2]) {
		t.Errorf("row above the status row is not the editor's bottom rule: %q", lines[len(lines)-2])
	}

	// No spinner row while idle: the first line is the editor's top rule,
	// not a spinner line.
	if strings.Contains(lines[0], "esc to stop") {
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

// TestStatusRow_ExactTextPerMode checks every mode's exact rendered label
// against the kiln design handoff's status-line contract: a "●" lead-in,
// coloured by mode (ask/manual dim, the auto-edit family green, plan
// blue), followed by the mode label and the mode-cycle key.
func TestStatusRow_ExactTextPerMode(t *testing.T) {
	cases := []struct {
		mode string
		want string
	}{
		{"auto", "auto-edit"},
		{"manual", "ask before edits"},
		{"acceptEdits", "auto-edit"},
		{"plan", "plan only"},
		{"bypassPermissions", "bypass permissions"},
		{"dontAsk", "don't ask"},
	}
	for _, c := range cases {
		m := newTestModel()
		mode := c.mode
		m.footer.Apply(StatusPatch{Mode: &mode})
		got := ansiStrip(m.renderStatusRow(200))
		if !strings.Contains(got, "●") || !strings.Contains(got, c.want) || !strings.Contains(got, "⇧⇥") {
			t.Errorf("mode %q: status row = %q, want it to contain %q, the mode dot and the cycle key", c.mode, got, c.want)
		}
	}
}

// TestCtrlC_ReplacesStatusRowForOneSecond drives Ctrl+C through handleKey
// (real wiring, not a synthetic Router) and checks the status row becomes
// "Press Ctrl-C again to exit" (ctrl-c-hint.txt) until the scheduled
// msgClearModeHint arrives, then is restored.
func TestCtrlC_ReplacesStatusRowForOneSecond(t *testing.T) {
	m := newTestModel()
	mode := "manual"
	m.footer.Apply(StatusPatch{Mode: &mode})

	mi, cmd := m.handleKey(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = mi.(Model)
	if got := ansiStrip(m.renderStatusRow(200)); got != "  Press Ctrl-C again to exit" {
		t.Fatalf("status row after ctrl+c = %q, want the Ctrl-C hint", got)
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
	if got := ansiStrip(m.renderStatusRow(200)); got == "  Press Ctrl-C again to exit" {
		t.Fatalf("status row still shows the Ctrl-C hint after msgClearModeHint: %q", got)
	}
}

// TestStatusRow_CtrlYAfterKillUntilNextKeystroke checks the status row
// switches to "Ctrl+Y to paste deleted text" for exactly the frame right
// after a Ctrl+K/Ctrl+U kill, and reverts on the very next keystroke
// (mode-manual.txt row 8: "until the next keystroke").
func TestStatusRow_CtrlYAfterKillUntilNextKeystroke(t *testing.T) {
	m := newTestModel()
	m.width, m.height = 100, 30
	m.editor.Focus()
	m.editor.SetValue("hello")

	if got := ansiStrip(m.renderStatusRow(100)); strings.Contains(got, "Ctrl+Y") {
		t.Fatalf("status row before any kill = %q, want no Ctrl+Y hint", got)
	}

	mi, _ := m.handleKey(tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	m = mi.(Model)
	if got := strings.TrimSpace(ansiStrip(m.renderStatusRow(100))); got != "Ctrl+Y to paste deleted text" {
		t.Fatalf("status row right after ctrl+k = %q, want the Ctrl+Y hint", got)
	}

	mi, _ = m.handleKey(charKey('!'))
	m = mi.(Model)
	if got := ansiStrip(m.renderStatusRow(100)); strings.Contains(got, "Ctrl+Y") {
		t.Fatalf("status row survived a keystroke after the kill: %q", got)
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

// TestFooter_AlwaysOneRowFittingWidth checks FooterState.RenderLine always
// produces exactly one row that fits, at a range of widths, busy or not
// (RenderLine itself does not vary with SetBusy any more — the busy hint
// moved to the busy line — but the row must still fit at every width).
func TestFooter_AlwaysOneRowFittingWidth(t *testing.T) {
	f := NewFooterState(StatusState{ModelLabel: "m", ContextWindow: 32000, Mode: "manual", StartedAt: time.Unix(0, 0)})
	for _, width := range []int{20, 40, 80, 117, 118, 144} {
		for _, busy := range []bool{false, true} {
			f.SetBusy(busy)
			row := f.RenderLine(width)
			if row == "" {
				t.Fatalf("width %d busy %v: row empty", width, busy)
			}
			if VisibleWidth(row) > width {
				t.Fatalf("width %d busy %v: row exceeds width: %q", width, busy, row)
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
	// so a popup row naming "model" must precede the editor's own top
	// rule. The popup now draws its own `─` rule directly above its rows
	// too (docs/kiln-design-handoff/README.md "Screen anatomy"), so the
	// frame has two full-width rules here — the editor's own top rule is
	// the one immediately above the prompt glyph row, not necessarily the
	// first rule in the frame.
	markerIdx := -1
	for i, l := range lines {
		if strings.Contains(l, "›") {
			markerIdx = i
			break
		}
	}
	if markerIdx == -1 {
		t.Fatalf("could not find the editor's prompt row in the frame:\n%s", strings.Join(lines, "\n"))
	}
	topRuleIdx := -1
	for i := markerIdx - 1; i >= 0; i-- {
		if isFullRule(lines[i]) {
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

// --- fullscreen mode (docs/kiln-fullscreen-plan.md) -------------------------

// newFullscreenTestModel builds a fullscreen Model and drives it through a
// real WindowSizeMsg (not just setting m.width/m.height directly), since
// the fullscreen path's viewport sizing/banner commit both live in that
// message's handler.
func newFullscreenTestModel(t *testing.T, w, h int) Model {
	t.Helper()
	m := NewModel(Config{
		Cwd:         "/tmp",
		ModelLabel:  "ollama/qwen3.8",
		InitialMode: "manual",
		StartedAt:   time.Unix(0, 0),
		Fullscreen:  true,
	})
	mi, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m, ok := mi.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", mi)
	}
	if !m.fullscreen {
		t.Fatalf("expected m.fullscreen after NewModel with Config.Fullscreen=true")
	}
	return m
}

func manyLines(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i)
	}
	return strings.Join(lines, "\n")
}

// TestFullscreen_AppendFollowsBottom checks a fullscreen append follows the
// viewport to the bottom, that the composed frame is exactly the terminal's
// height, that the last row is the mode line, and that the view carries
// AltScreen.
func TestFullscreen_AppendFollowsBottom(t *testing.T) {
	m := newFullscreenTestModel(t, 80, 10)

	mi, _ := m.Update(MsgTranscriptAppend{Text: manyLines(30)})
	m = mi.(Model)

	if !m.viewport.AtBottom() {
		t.Fatalf("viewport not at bottom after an append with no prior scroll")
	}

	v := m.View()
	rows := strings.Split(v.Content, "\n")
	if len(rows) != 10 {
		t.Fatalf("frame has %d rows, want 10 (the terminal height):\n%s", len(rows), v.Content)
	}
	last := ansiStrip(rows[len(rows)-1])
	if !strings.Contains(last, "ask before edits") {
		t.Errorf("last row = %q, want the status line", last)
	}
	if !v.AltScreen {
		t.Errorf("AltScreen = false, want true in fullscreen")
	}
}

// TestFullscreen_ScrollPauses checks PgUp leaves the viewport off the
// bottom, that a subsequent append does not yank it back down
// (scroll-to-pause), and that PgDn returns it to the bottom.
func TestFullscreen_ScrollPauses(t *testing.T) {
	m := newFullscreenTestModel(t, 80, 10)

	mi, _ := m.Update(MsgTranscriptAppend{Text: manyLines(30)})
	m = mi.(Model)

	mi, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyPgUp})
	m = mi.(Model)
	if m.viewport.AtBottom() {
		t.Fatalf("expected the viewport off the bottom after pgup")
	}
	off := m.viewport.YOffset()

	mi, _ = m.Update(MsgTranscriptAppend{Text: manyLines(5)})
	m = mi.(Model)
	if got := m.viewport.YOffset(); got != off {
		t.Fatalf("YOffset changed from %d to %d after an append while scrolled up; scroll-to-pause should hold it", off, got)
	}

	for i := 0; i < 20 && !m.viewport.AtBottom(); i++ {
		mi, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
		m = mi.(Model)
	}
	if !m.viewport.AtBottom() {
		t.Fatalf("expected the viewport back at the bottom after pgdown")
	}
}

// TestFullscreen_ClearResetsTranscript checks tea.ClearScreen empties the
// fullscreen transcript buffer (the same message a Ctrl+O/toggle/resize
// sequence sends ahead of its own replay).
func TestFullscreen_ClearResetsTranscript(t *testing.T) {
	m := newFullscreenTestModel(t, 80, 10)

	mi, _ := m.Update(MsgTranscriptAppend{Text: manyLines(5)})
	m = mi.(Model)
	if len(m.transcript) == 0 {
		t.Fatalf("expected a non-empty transcript before clearing")
	}

	mi, _ = m.Update(tea.ClearScreen())
	m = mi.(Model)
	if len(m.transcript) != 0 {
		t.Fatalf("transcript not reset by tea.ClearScreen: %v", m.transcript)
	}
}

// TestFullscreen_PlainFallsBackInline checks Config{Fullscreen: true, Plain:
// true} falls back to inline (docs/kiln-fullscreen-plan.md: "fullscreen
// falls back to inline" under screen-reader mode).
func TestFullscreen_PlainFallsBackInline(t *testing.T) {
	SetPlainMode(true)
	defer SetPlainMode(false)

	m := NewModel(Config{
		Cwd:         "/tmp",
		ModelLabel:  "ollama/qwen3.8",
		InitialMode: "manual",
		StartedAt:   time.Unix(0, 0),
		Fullscreen:  true,
		Plain:       true,
	})
	if m.fullscreen {
		t.Fatalf("expected m.fullscreen=false with Plain=true")
	}
	mi, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 10})
	m = mi.(Model)
	if v := m.View(); v.AltScreen {
		t.Errorf("View().AltScreen = true, want false with Plain=true")
	}
}

// TestRouter_CtrlF_ToggleFullscreen checks ctrl+f fires ToggleFullscreen
// when wired, and falls through unconsumed when it is nil (so a test that
// does not wire it is unaffected).
func TestRouter_CtrlF_ToggleFullscreen(t *testing.T) {
	r, router, _ := routed()
	router.actions.ToggleFullscreen = func() { r.calls = append(r.calls, "toggleFullscreen") }
	if !router.Route(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl}) {
		t.Fatalf("ctrl+f not consumed with ToggleFullscreen wired")
	}
	if !r.has("toggleFullscreen") {
		t.Errorf("ToggleFullscreen was not called")
	}

	_, router2, _ := routed()
	if router2.Route(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl}) {
		t.Fatalf("ctrl+f consumed with ToggleFullscreen nil")
	}
}

// TestApp_PromptLayout_BusyLineInputStatusBelowPrompt checks this pass's
// layout change to liveLines: a tool-permission prompt used to replace the
// input box and mode line outright (permission-edit.txt); now it renders
// in that same transcript position, but the busy line, the editor and the
// status row all keep rendering below it too (docs/kiln-design-handoff/
// README.md scene 06: "approval needed" block, then "◐ Waiting for
// approval…", then the input with "press 1, 2 or 3", then the status
// line). This checks the frame contains, in that exact order: the prompt
// block, then a busy row ("esc to stop"), then the editor's own prompt
// glyph row, then the status row (the last line in the frame).
func TestApp_PromptLayout_BusyLineInputStatusBelowPrompt(t *testing.T) {
	m := newTestModel()
	m.busy = true
	m.spinner.Start(0)
	m.spinner.SetLabel("Waiting for approval")
	m.prompt.pending = &pendingPermission{
		request: PermissionRequest{ToolName: "grep", PrimaryArg: "TODO"},
	}
	m = m.syncPromptPlaceholder()

	lines := viewLines(m)

	promptIdx := indexContaining(lines, "Allow kiln to use grep?")
	busyIdx := indexContaining(lines, "esc to stop")
	editorIdx := indexContaining(lines, "›")
	statusIdx := -1
	for i, l := range lines {
		if strings.TrimSpace(l) != "" {
			statusIdx = i
		}
	}

	if promptIdx == -1 {
		t.Fatalf("prompt block missing from the frame:\n%s", strings.Join(lines, "\n"))
	}
	if busyIdx == -1 {
		t.Fatalf("busy row (\"esc to stop\") missing while a prompt is active:\n%s", strings.Join(lines, "\n"))
	}
	if editorIdx == -1 {
		t.Fatalf("editor row missing while a prompt is active:\n%s", strings.Join(lines, "\n"))
	}
	if statusIdx == -1 {
		t.Fatalf("status row missing while a prompt is active:\n%s", strings.Join(lines, "\n"))
	}
	if !(promptIdx < busyIdx && busyIdx < editorIdx && editorIdx < statusIdx) {
		t.Errorf("wrong order: prompt=%d busy=%d editor=%d status=%d (want strictly increasing):\n%s",
			promptIdx, busyIdx, editorIdx, statusIdx, strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[editorIdx], "press 1, 2 or 3") {
		t.Errorf("editor row %q missing the \"press 1, 2 or 3\" placeholder", lines[editorIdx])
	}
}

// indexContaining returns the index of the first row containing needle, or
// -1.
func indexContaining(lines []string, needle string) int {
	for i, l := range lines {
		if strings.Contains(l, needle) {
			return i
		}
	}
	return -1
}
