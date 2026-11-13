package tui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/mods"
)

// fakePromptEngine records the prompt events the TUI raises.
type fakePromptEngine struct {
	edits   chan editEvent
	changes chan changeEvent
	rows    []mods.PromptSuggestion
}

type editEvent struct {
	before     mods.PromptBox
	start, end int
	input      string
}

type changeEvent struct {
	box    mods.PromptBox
	origin map[string]any
}

func newFakePromptEngine() *fakePromptEngine {
	return &fakePromptEngine{edits: make(chan editEvent, 8), changes: make(chan changeEvent, 8)}
}

func (f *fakePromptEngine) Render(context.Context, mods.Site) []mods.Drawing { return nil }
func (f *fakePromptEngine) Press(mods.Callback)                              {}
func (f *fakePromptEngine) Has(string) bool                                  { return false }
func (f *fakePromptEngine) PersonScroll(context.Context, map[string]any) (int, bool) {
	return 0, false
}
func (f *fakePromptEngine) PersonFocus(context.Context, map[string]any) (string, bool) {
	return "", false
}
func (f *fakePromptEngine) PromptEdit(_ context.Context, before mods.PromptBox, start, end int, in string) {
	f.edits <- editEvent{before, start, end, in}
}
func (f *fakePromptEngine) PromptChange(_ context.Context, box mods.PromptBox, origin map[string]any) {
	f.changes <- changeEvent{box, origin}
}
func (f *fakePromptEngine) PromptAutocomplete(context.Context, mods.PromptBox, string, int) []mods.PromptSuggestion {
	return f.rows
}

func modsPromptModel(t *testing.T) (Model, *fakePromptEngine, *ModsDriver) {
	t.Helper()
	eng := newFakePromptEngine()
	d := NewModsDriver(eng)
	t.Cleanup(d.Close)
	d.SetSend(func(tea.Msg) {}, nil)
	m := NewModel(Config{Cwd: "/tmp", ModelLabel: "m", InitialMode: "manual", StartedAt: time.Unix(0, 0), Mods: d})
	m.width, m.height = 80, 24
	return m, eng, d
}

func waitEdit(t *testing.T, f *fakePromptEngine) editEvent {
	t.Helper()
	select {
	case e := <-f.edits:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("no prompt.edit raised")
	}
	return editEvent{}
}

func waitChange(t *testing.T, f *fakePromptEngine) changeEvent {
	t.Helper()
	select {
	case e := <-f.changes:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("no prompt.change raised")
	}
	return changeEvent{}
}

func TestTypingRaisesPromptEditAndPublishesTheBox(t *testing.T) {
	m, eng, d := modsPromptModel(t)
	m = press(t, m, "h")
	_ = press(t, m, "i")
	e1 := waitEdit(t, eng)
	if e1.start != 0 || e1.end != 0 || e1.input != "h" || e1.before.Text != "" {
		t.Fatalf("first edit = %+v, want an insert of h into an empty box", e1)
	}
	e2 := waitEdit(t, eng)
	if e2.start != 1 || e2.end != 1 || e2.input != "i" || e2.before.Text != "h" || e2.before.Cursor != 1 {
		t.Fatalf("second edit = %+v, want an insert of i at 1", e2)
	}
	if box, ok := d.Box(); !ok || box.Text != "hi" || box.Cursor != 2 {
		t.Fatalf("published box = %+v ok=%v, want hi with the cursor at 2", box, ok)
	}
	select {
	case c := <-eng.changes:
		t.Fatalf("typing raised prompt.change %+v; that is prompt.edit", c)
	default:
	}
}

func TestFillRaisesPromptChangeFromThePlugin(t *testing.T) {
	m, eng, d := modsPromptModel(t)
	reply := make(chan mods.PromptFillAnswer, 1)
	m = updateModel(t, m, MsgModsFill{Text: "hello", Mode: "replace", Plugin: "p", Reply: reply})
	a := gotReply(t, "fill", reply)
	if !a.Filled || a.Box.Text != "hello" || a.Box.Cursor != 5 {
		t.Fatalf("fill answer = %+v", a)
	}
	c := waitChange(t, eng)
	if c.box.Text != "hello" || c.origin["kind"] != "plugin" || c.origin["name"] != "p" {
		t.Fatalf("change = %+v, want origin plugin p", c)
	}
	if box, _ := d.Box(); box.Text != "hello" {
		t.Fatalf("published box = %+v", box)
	}

	reply = make(chan mods.PromptFillAnswer, 1)
	m = updateModel(t, m, MsgModsFill{Text: " world", Mode: "append", Plugin: "p", Reply: reply})
	if a := gotReply(t, "append fill", reply); a.Box.Text != "hello world" {
		t.Fatalf("append = %+v", a)
	}
	reply = make(chan mods.PromptFillAnswer, 1)
	_ = updateModel(t, m, MsgModsFill{Text: "X", Mode: "insert", Plugin: "p", Reply: reply})
	if a := gotReply(t, "insert fill", reply); a.Box.Text != "hello worldX" || a.Box.Cursor != 12 {
		t.Fatalf("insert at the cursor = %+v", a)
	}
}

func TestFillRefusedWhileADialogHoldsTheKeys(t *testing.T) {
	m, _, _ := modsPromptModel(t)
	m.busy = true
	m = updateModel(t, m, MsgPermissionPrompt{Request: PermissionRequest{ToolName: "bash", PrimaryArg: "ls"}, Reply: make(chan PromptChoice, 1)})
	reply := make(chan mods.PromptFillAnswer, 1)
	_ = updateModel(t, m, MsgModsFill{Text: "x", Mode: "replace", Plugin: "p", Reply: reply})
	if a := gotReply(t, "fill", reply); a.Filled || a.Refusal != "dialog" {
		t.Fatalf("fill under a dialog = %+v, want the dialog refusal", a)
	}
}

func TestSuggestShowsOnlyInAnIdleEmptyBox(t *testing.T) {
	m, _, _ := modsPromptModel(t)
	reply := make(chan bool, 1)
	m = updateModel(t, m, MsgModsSuggest{Text: "  run the tests ", Reply: reply})
	if !gotReply(t, "suggest", reply) {
		t.Fatal("an idle empty box refused the suggestion")
	}
	if m.editor.Suggestion() != "run the tests" {
		t.Fatalf("suggestion = %q", m.editor.Suggestion())
	}

	busy := m
	busy.busy = true
	reply = make(chan bool, 1)
	busy = updateModel(t, busy, MsgModsSuggest{Text: "x", Reply: reply})
	if gotReply(t, "busy suggest", reply) {
		t.Fatal("a busy session showed a suggestion")
	}

	typed := press(t, m, "z")
	reply = make(chan bool, 1)
	_ = updateModel(t, typed, MsgModsSuggest{Text: "x", Reply: reply})
	if gotReply(t, "typed suggest", reply) {
		t.Fatal("a box with a draft showed a suggestion")
	}
}

func TestSubmitQueuesBehindARunningTurn(t *testing.T) {
	m, _, _ := modsPromptModel(t)
	m.busy = true
	reply := make(chan error, 1)
	m = updateModel(t, m, MsgModsSubmit{Text: "go", Plugin: "p", Reply: reply})
	if err := gotReply(t, "submit", reply); err != nil {
		t.Fatalf("submit err = %v", err)
	}
	if len(m.pluginPrompts) != 1 || m.pluginPrompts[0].text != "go" {
		t.Fatalf("queue = %+v, want the prompt held until the turn ends", m.pluginPrompts)
	}
}

func TestModAutocompleteRowsJoinThePopup(t *testing.T) {
	m, eng, _ := modsPromptModel(t)
	eng.rows = []mods.PromptSuggestion{{Text: "#123", Label: "issue 123", Description: "a bug"}}
	m.editor.SetValueCursor("see #", 5)
	cmd := m.modAutocompleteCmd()
	if cmd == nil {
		t.Fatal("no autocomplete request for a token at the cursor")
	}
	msg, ok := cmd().(msgModsAutocomplete)
	if !ok {
		t.Fatal("the mods' rows did not come back as a message")
	}
	m = m.applyModAutocomplete(msg)
	if m.popup == nil || len(m.popup.Items) != 1 || !m.popup.Items[0].Mod || m.popup.Items[0].Value != "#123" {
		t.Fatalf("popup = %+v, want the mod's row", m.popup)
	}
	if s, e := m.popup.Range(); s != 4 || e != 5 {
		t.Fatalf("range = %d..%d, want the typed token 4..5", s, e)
	}

	// The person typed on since: the rows are stale and dropped.
	stale := newTestModel()
	stale.editor.SetValueCursor("see #x", 6)
	if got := stale.applyModAutocomplete(msg); got.popup != nil {
		t.Fatalf("stale rows were applied: %+v", got.popup)
	}
}

func TestEditorCauseAndCursorOffset(t *testing.T) {
	m := newTestModel()
	m.editor.SetValueCursor("héllo 😀", 3)
	if got := m.editor.CursorOffset(); got != 3 {
		t.Fatalf("CursorOffset = %d, want 3 (runes)", got)
	}
	if m.editor.Cause() != "" {
		t.Fatalf("SetValueCursor recorded cause %q; the caller names it", m.editor.Cause())
	}
	m.editor.SetValue("abc")
	if m.editor.Cause() != "engine" {
		t.Fatalf("cause after SetValue = %q, want engine", m.editor.Cause())
	}
	m.editor.ClearCause()
	if m.editor.Cause() != "" {
		t.Fatal("ClearCause left a cause")
	}
	m.editor.SetValue("")
	if m.editor.Cause() != "clear" {
		t.Fatalf("cause after an empty SetValue = %q, want clear", m.editor.Cause())
	}
}
