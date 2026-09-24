package tui

import (
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
	default:
		r := []rune(s)
		return tea.KeyPressMsg{Code: r[0], Text: s}
	}
}

func TestModalView_EscCloses(t *testing.T) {
	v := NewModalView(fakeModalSpec())
	consumed, shouldClose, _ := v.HandleKey(key("esc"))
	if !consumed || !shouldClose {
		t.Fatalf("esc: consumed=%v shouldClose=%v, want true,true", consumed, shouldClose)
	}
}

func TestModalView_EnterSelects(t *testing.T) {
	v := NewModalView(fakeModalSpec())
	runKey(v, key("enter"))
	if v.status != "selected a" {
		t.Errorf("status = %q, want %q", v.status, "selected a")
	}
}

func TestModalView_DownMovesCursorThenEnterSelectsSecond(t *testing.T) {
	v := NewModalView(fakeModalSpec())
	runKey(v, key("down"))
	if v.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", v.cursor)
	}
	runKey(v, key("enter"))
	if v.status != "selected b" {
		t.Errorf("status = %q, want %q", v.status, "selected b")
	}
}

func TestModalView_ActionKeyRunsAct(t *testing.T) {
	v := NewModalView(fakeModalSpec())
	consumed, shouldClose, cmd := v.HandleKey(key("d"))
	if !consumed || shouldClose {
		t.Fatalf("consumed=%v shouldClose=%v, want true,false", consumed, shouldClose)
	}
	if cmd == nil {
		t.Fatal("expected an Act command")
	}
	v.Apply(cmd().(msgModalResult))
	if v.status != "acted d on a" {
		t.Errorf("status = %q, want %q", v.status, "acted d on a")
	}
}

func TestModalView_UnknownKeyIsSwallowed(t *testing.T) {
	v := NewModalView(fakeModalSpec())
	consumed, shouldClose, _ := v.HandleKey(key("z"))
	if !consumed || shouldClose {
		t.Fatalf("consumed=%v shouldClose=%v, want true,false (swallowed)", consumed, shouldClose)
	}
}

// runKey presses a key and, when it yields a Select/Act command, runs it
// synchronously and applies the result, as the app's Update would.
func runKey(v *ModalView, k tea.KeyPressMsg) {
	if _, _, cmd := v.HandleKey(k); cmd != nil {
		if r, ok := cmd().(msgModalResult); ok {
			v.Apply(r)
		}
	}
}
