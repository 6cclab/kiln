package tui

import (
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// keyMsg builds a tea.KeyPressMsg for the handful of named keys these
// dialog tests drive, matching the Code values the app's real key
// dispatch uses (see internal/tui/keys_test.go).
func keyMsg(name string) tea.KeyPressMsg {
	switch name {
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	default:
		r := []rune(name)[0]
		return tea.KeyPressMsg{Code: r, Text: name}
	}
}

// TestRewindDialog_MatchesReferenceRows pins Render's body rows for one
// user-message entry plus the "(current)" option, per the kiln design
// (docs/kiln-design.md): no dialogIndent (content aligns with every other
// block's left edge — DialogTopRule's "rewind ────" label rule carries
// the indent instead, QA finding 20260927T000712Z-dialog-chrome-effort-
// indicator), a blank-or-"> " selectionGutter instead of "❯", and the
// selected row's raised background pads it to the full width.
func TestRewindDialog_MatchesReferenceRows(t *testing.T) {
	SetColorEnabled(false)
	defer SetColorEnabled(true)

	entries := []RewindEntry{
		{ID: "e1", Text: "run this shell command and report its output: echo parity-check", FilesChanged: 0},
	}
	d := NewRewindDialog(entries, nil)
	got := d.Render(100, 40)

	want := []string{
		"Rewind",
		// kiln's rewind moves the conversation only; the reference's
		// "Restore the code and/or conversation" would promise more.
		"Go back in the conversation to before one of your messages. Files are not restored.",
		"  run this shell command and report its output: echo parity-check",
		"  No code changes",
		"> (current)",
		"Enter to continue · Esc to cancel",
	}

	if len(got) != len(want) {
		t.Fatalf("row count = %d, want %d\ngot:  %q\nwant: %q", len(got), len(want), got, want)
	}
	for i := range want {
		row := got[i]
		if i == 4 { // the selected "(current)" row carries the raised background
			row = strings.TrimRight(row, " ")
		}
		if row != want[i] {
			t.Errorf("row %d:\n got:  %q\n want: %q", i, row, want[i])
		}
	}
}

func TestRewindDialog_DefaultSelectionIsCurrent(t *testing.T) {
	entries := []RewindEntry{{ID: "e1", Text: "one"}, {ID: "e2", Text: "two"}}
	d := NewRewindDialog(entries, nil).(*rewindDialog)
	if d.cursor != len(entries) {
		t.Errorf("default cursor = %d, want %d (the \"(current)\" row)", d.cursor, len(entries))
	}
}

func TestRewindDialog_UpDownNavigates(t *testing.T) {
	entries := []RewindEntry{{ID: "e1", Text: "one"}, {ID: "e2", Text: "two"}}
	d := NewRewindDialog(entries, nil).(*rewindDialog)

	consumed, closeIt, _ := d.HandleKey(keyMsg("up"))
	if !consumed || closeIt {
		t.Fatalf("up: consumed=%v closeIt=%v", consumed, closeIt)
	}
	if d.cursor != 1 {
		t.Errorf("cursor after one up = %d, want 1", d.cursor)
	}
	d.HandleKey(keyMsg("up"))
	if d.cursor != 0 {
		t.Errorf("cursor after two ups = %d, want 0", d.cursor)
	}
	d.HandleKey(keyMsg("up")) // clamps at 0
	if d.cursor != 0 {
		t.Errorf("cursor should clamp at 0, got %d", d.cursor)
	}
	d.HandleKey(keyMsg("down"))
	d.HandleKey(keyMsg("down"))
	d.HandleKey(keyMsg("down")) // clamps at len(entries)
	if d.cursor != len(entries) {
		t.Errorf("cursor should clamp at %d, got %d", len(entries), d.cursor)
	}
}

func TestRewindDialog_EnterOnCurrentJustCloses(t *testing.T) {
	entries := []RewindEntry{{ID: "e1", Text: "one"}}
	called := false
	d := NewRewindDialog(entries, func(id string) error { called = true; return nil }).(*rewindDialog)
	// default cursor is already "(current)"
	consumed, closeIt, cmd := d.HandleKey(keyMsg("enter"))
	if !consumed || !closeIt {
		t.Fatalf("enter on (current): consumed=%v closeIt=%v", consumed, closeIt)
	}
	if cmd != nil {
		t.Error("enter on (current) should not produce a result command")
	}
	if called {
		t.Error("onSelect should not be called for (current)")
	}
}

func TestRewindDialog_EnterOnMessageCallsOnSelect(t *testing.T) {
	entries := []RewindEntry{{ID: "target-id", Text: "do the thing"}}
	var gotID string
	d := NewRewindDialog(entries, func(id string) error { gotID = id; return nil }).(*rewindDialog)
	d.cursor = 0 // the message row, not "(current)"

	consumed, closeIt, cmd := d.HandleKey(keyMsg("enter"))
	if !consumed || !closeIt {
		t.Fatalf("enter on message: consumed=%v closeIt=%v", consumed, closeIt)
	}
	if gotID != "target-id" {
		t.Errorf("onSelect got id %q, want %q", gotID, "target-id")
	}
	if cmd == nil {
		t.Fatal("expected a result command")
	}
	m, ok := cmd().(msgDialogResult)
	if !ok {
		t.Fatalf("cmd() = %T, want msgDialogResult", cmd())
	}
	if m.msg != "Rewound to before: do the thing" {
		t.Errorf("result msg = %q", m.msg)
	}
}

func TestRewindDialog_EscCloses(t *testing.T) {
	d := NewRewindDialog(nil, nil)
	_, closeIt, _ := d.HandleKey(keyMsg("esc"))
	if !closeIt {
		t.Error("esc should close")
	}
}

func TestRewindDialog_RenderAt100And60(t *testing.T) {
	SetColorEnabled(false)
	defer SetColorEnabled(true)

	entries := []RewindEntry{
		{ID: "e1", Text: "a short first message"},
		{ID: "e2", Text: "a much longer message that should still fit or get truncated cleanly depending on the terminal width available"},
		{ID: "e3", Text: "three files changed here", FilesChanged: 3},
	}
	for _, width := range []int{100, 60} {
		d := NewRewindDialog(entries, nil)
		rows := d.Render(width, 40)
		if len(rows) == 0 {
			t.Fatalf("width %d: no rows rendered", width)
		}
		for _, r := range rows {
			if VisibleWidth(r) > width {
				t.Errorf("width %d: row exceeds width: %q", width, r)
			}
		}
	}
}

func TestRewindEntriesFromSession_FilesChangedIsAlwaysZero(t *testing.T) {
	// Documents the known gap named in the task brief: Entry carries no
	// per-message file-diff count today, so FilesChanged is always 0 until
	// that data exists somewhere to read.
	entries := RewindEntriesFromSession(nil)
	for _, e := range entries {
		if e.FilesChanged != 0 {
			t.Errorf("FilesChanged = %d, want 0 (not implemented)", e.FilesChanged)
		}
	}
}

// TestRewindEntriesCountEditedFiles: each message's row counts the distinct
// files the turn after it edited or wrote, up to the next message.
func TestRewindEntriesCountEditedFiles(t *testing.T) {
	user := func(id string, seq int64, text string) session.Entry {
		return session.Entry{ID: id, Seq: seq, Type: session.EntryMessage, Message: msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text(text)}}}
	}
	calls := func(id string, seq int64, cs ...msg.ToolCall) session.Entry {
		var b msg.Blocks
		for _, c := range cs {
			b = append(b, c)
		}
		return session.Entry{ID: id, Seq: seq, Type: session.EntryMessage, Message: msg.AssistantMessage{Content: b}}
	}
	entries := []session.Entry{
		user("u1", 1, "add a doc comment"),
		calls("a1", 2, msg.ToolCall{Name: "read", Arguments: map[string]any{"path": "g.go"}}, msg.ToolCall{Name: "edit", Arguments: map[string]any{"path": "g.go"}}),
		calls("a2", 3, msg.ToolCall{Name: "edit", Arguments: map[string]any{"path": "g.go"}}, msg.ToolCall{Name: "write", Arguments: map[string]any{"path": "h.go"}}),
		user("u2", 4, "what changed?"),
	}
	got := RewindEntriesFromSession(entries)
	if len(got) != 2 || got[0].FilesChanged != 2 || got[1].FilesChanged != 0 {
		t.Errorf("entries = %+v, want 2 files for the first turn and 0 for the second", got)
	}
}
