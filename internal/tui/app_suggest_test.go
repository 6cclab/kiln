package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/claude/permission"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/harness"
)

// runToMsg runs a tea.Cmd (flattening a BatchMsg) and returns the first
// message of type T it produces, or the zero value if none.
func collectMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	var out []tea.Msg
	m := cmd()
	switch mm := m.(type) {
	case tea.BatchMsg:
		for _, c := range mm {
			out = append(out, collectMsgs(c)...)
		}
	case nil:
	default:
		out = append(out, m)
	}
	return out
}

func firstSuggestion(msgs []tea.Msg) (msgSuggestion, bool) {
	for _, m := range msgs {
		if s, ok := m.(msgSuggestion); ok {
			return s, true
		}
	}
	return msgSuggestion{}, false
}

func TestFinishTurn_SuggestsAfterCompletedTurn(t *testing.T) {
	m, _ := newTestModelWithBridge(t)
	m.cfg.SuggestNextPrompt = func(context.Context) (string, error) {
		return "  add a regression test  ", nil
	}
	m.busy = true

	nm, cmd := m.finishTurn(msgTurnResult{result: harness.RunResult{Status: harness.StatusCompleted}})

	s, ok := firstSuggestion(collectMsgs(cmd))
	if !ok {
		t.Fatal("finishTurn did not emit a msgSuggestion after a completed turn")
	}
	if s.text != "add a regression test" {
		t.Fatalf("suggestion text = %q, want trimmed value", s.text)
	}
	if s.gen != nm.turn {
		t.Fatalf("suggestion gen = %d, want current turn %d", s.gen, nm.turn)
	}
}

func TestFinishTurn_NoSuggestionWhenBoxHasText(t *testing.T) {
	m, _ := newTestModelWithBridge(t)
	m.cfg.SuggestNextPrompt = func(context.Context) (string, error) { return "x", nil }
	m.busy = true
	m.editor.SetValue("already typing")

	_, cmd := m.finishTurn(msgTurnResult{result: harness.RunResult{Status: harness.StatusCompleted}})
	if _, ok := firstSuggestion(collectMsgs(cmd)); ok {
		t.Fatal("suggested while the input box already had text")
	}
}

func TestFinishTurn_NoSuggestionOnError(t *testing.T) {
	m, _ := newTestModelWithBridge(t)
	m.cfg.SuggestNextPrompt = func(context.Context) (string, error) { return "x", nil }
	m.busy = true

	_, cmd := m.finishTurn(msgTurnResult{result: harness.RunResult{Status: harness.StatusCompleted}, err: context.Canceled})
	if _, ok := firstSuggestion(collectMsgs(cmd)); ok {
		t.Fatal("suggested after a turn that ended in an error")
	}
}

func TestFinishTurn_NoSuggestionWhenAborted(t *testing.T) {
	m, _ := newTestModelWithBridge(t)
	m.cfg.SuggestNextPrompt = func(context.Context) (string, error) { return "x", nil }
	m.busy = true

	_, cmd := m.finishTurn(msgTurnResult{result: harness.RunResult{Status: harness.StatusAborted}})
	if _, ok := firstSuggestion(collectMsgs(cmd)); ok {
		t.Fatal("suggested after an aborted turn")
	}
}

func TestFinishTurn_NoSuggestionInPlanMode(t *testing.T) {
	m, _ := newTestModelWithBridge(t)
	m.cfg.SuggestNextPrompt = func(context.Context) (string, error) { return "x", nil }
	m.cfg.Gate = permission.NewGate(permission.GateOptions{Mode: claudesettings.ModePlan})
	m.busy = true

	_, cmd := m.finishTurn(msgTurnResult{result: harness.RunResult{Status: harness.StatusCompleted}})
	if _, ok := firstSuggestion(collectMsgs(cmd)); ok {
		t.Fatal("suggested while in plan mode")
	}
}

func TestMsgSuggestion_SetsGhostWhenFresh(t *testing.T) {
	m, _ := newTestModelWithBridge(t)
	next, _ := m.update(msgSuggestion{text: "try the next thing", gen: m.turn})
	nm := next.(Model)
	if got := nm.editor.Suggestion(); got != "try the next thing" {
		t.Fatalf("editor suggestion = %q, want it set", got)
	}
}

func TestMsgSuggestion_DroppedWhenStale(t *testing.T) {
	m, _ := newTestModelWithBridge(t)
	// A later turn has begun: gen no longer matches.
	next, _ := m.update(msgSuggestion{text: "stale", gen: m.turn - 1})
	if got := next.(Model).editor.Suggestion(); got != "" {
		t.Fatalf("stale suggestion was shown: %q", got)
	}
}

func TestMsgSuggestion_DroppedWhenBusy(t *testing.T) {
	m, _ := newTestModelWithBridge(t)
	m.busy = true
	next, _ := m.update(msgSuggestion{text: "mid-turn", gen: m.turn})
	if got := next.(Model).editor.Suggestion(); got != "" {
		t.Fatalf("suggestion shown while busy: %q", got)
	}
}

func TestMsgSuggestion_DroppedWhenBoxNonEmpty(t *testing.T) {
	m, _ := newTestModelWithBridge(t)
	m.editor.SetValue("typing")
	next, _ := m.update(msgSuggestion{text: "late", gen: m.turn})
	if got := next.(Model).editor.Suggestion(); got != "" {
		t.Fatalf("suggestion shown over typed text: %q", got)
	}
}
