package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/commands"
)

func fakeModalSpec() commands.ModalSpec {
	return commands.ModalSpec{
		Title: "Models",
		Items: []commands.Item{
			{Value: "a", Label: "alpha", Description: "the first"},
			{Value: "b", Label: "beta"},
		},
		Actions: []commands.Action{{Key: "d", Label: "delete"}},
		Select:  func(value string) (string, error) { return "selected " + value, nil },
		Act:     func(key, value string) (string, error) { return "acted " + key + " on " + value, nil },
	}
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	default:
		r := []rune(s)
		return tea.KeyPressMsg{Code: r[0], Text: s}
	}
}

func TestCommandDialog_EscClosesWithSpec(t *testing.T) {
	v := NewCommandDialog(fakeModalSpec()).(*commandDialog)
	consumed, shouldClose, _ := v.HandleKey(key("esc"))
	if !consumed || !shouldClose {
		t.Fatalf("esc: consumed=%v shouldClose=%v, want true,true", consumed, shouldClose)
	}
}

func TestCommandDialog_EnterSelects(t *testing.T) {
	v := NewCommandDialog(fakeModalSpec()).(*commandDialog)
	runKey(v, key("enter"))
	if v.status != "selected a" {
		t.Errorf("status = %q, want %q", v.status, "selected a")
	}
}

func TestCommandDialog_DownMovesCursorThenEnterSelectsSecond(t *testing.T) {
	v := NewCommandDialog(fakeModalSpec()).(*commandDialog)
	runKey(v, key("down"))
	if v.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", v.cursor)
	}
	runKey(v, key("enter"))
	if v.status != "selected b" {
		t.Errorf("status = %q, want %q", v.status, "selected b")
	}
}

func TestCommandDialog_ActionKeyRunsAct(t *testing.T) {
	v := NewCommandDialog(fakeModalSpec()).(*commandDialog)
	consumed, shouldClose, cmd := v.HandleKey(key("d"))
	if !consumed || shouldClose {
		t.Fatalf("consumed=%v shouldClose=%v, want true,false", consumed, shouldClose)
	}
	if cmd == nil {
		t.Fatal("expected an Act command")
	}
	v.Apply(cmd().(msgDialogResult))
	if v.status != "acted d on a" {
		t.Errorf("status = %q, want %q", v.status, "acted d on a")
	}
}

func TestCommandDialog_UnknownKeyIsSwallowed(t *testing.T) {
	v := NewCommandDialog(fakeModalSpec()).(*commandDialog)
	consumed, shouldClose, _ := v.HandleKey(key("z"))
	if !consumed || shouldClose {
		t.Fatalf("consumed=%v shouldClose=%v, want true,false (swallowed)", consumed, shouldClose)
	}
}

// runKey presses a key and, when it yields a Select/Act command, runs it
// synchronously and applies the result, as the app's Update would.
func runKey(v *commandDialog, k tea.KeyPressMsg) {
	if _, _, cmd := v.HandleKey(k); cmd != nil {
		if r, ok := cmd().(msgDialogResult); ok {
			v.Apply(r)
		}
	}
}

// TestCommandDialog_ActRefreshesHeader: a header row reporting state the
// action changed is rebuilt once the result lands, so the open panel does
// not show the old value (qa/findings *permissions-mode-row-stale).
func TestCommandDialog_ActRefreshesHeader(t *testing.T) {
	mode := "manual"
	spec := fakeModalSpec()
	spec.Header = []string{"mode       " + mode}
	spec.RefreshHeader = func() []string { return []string{"mode       " + mode} }
	spec.Act = func(key, value string) (string, error) { mode = "acceptEdits"; return "mode is now " + mode, nil }
	v := NewCommandDialog(spec).(*commandDialog)
	_, _, cmd := v.HandleKey(key("d"))
	v.Apply(cmd().(msgDialogResult))
	if got := strings.Join(v.Render(80, 20), "\n"); !strings.Contains(got, "acceptEdits") || strings.Contains(got, "mode       manual") {
		t.Errorf("header not refreshed after the action:\n%s", got)
	}
}
