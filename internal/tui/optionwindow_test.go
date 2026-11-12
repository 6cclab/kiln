package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/tools"
)

func longChoice(n int) []tools.AskUserOption {
	opts := make([]tools.AskUserOption, n)
	for i := range opts {
		opts[i] = tools.AskUserOption{Label: fmt.Sprintf("provider-%02d", i+1)}
	}
	return opts
}

// A closed choice with dozens of options (the /login picker) shows a window
// of them around the highlighted row, keeps the question on screen, and
// follows the arrows.
func TestLongOptionListScrollsInAWindow(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := make(chan AskUserReply, 1)
	p.enqueue(queuedPrompt{question: newPendingQuestion([]tools.AskUserQuestion{{Question: "Which provider?", Options: longChoice(40)}}, nil, true, reply)})
	out := renderedText(p)
	if !strings.Contains(out, "Which provider?") {
		t.Fatalf("the question scrolled away:\n%s", out)
	}
	if n := len(p.Render(80)); n > 20 {
		t.Fatalf("a 40-option question renders %d rows", n)
	}
	if !strings.Contains(out, "↓ 33 more") || strings.Count(out, " more") != 1 {
		t.Fatalf("top of the list should only have a below marker:\n%s", out)
	}
	for i := 0; i < 30; i++ {
		p.HandleKey(key("down"))
	}
	out = renderedText(p)
	if !strings.Contains(out, "provider-31") || strings.Count(out, " more") != 2 {
		t.Fatalf("window did not follow the selection:\n%s", out)
	}
	p.HandleKey(key("enter"))
	if r := <-reply; r.Answers[0].Answers[0] != "provider-31" {
		t.Fatalf("answer = %+v, want provider-31", r)
	}
}

func TestPlaceholderForLongListsPointsAtTheArrows(t *testing.T) {
	if got := placeholderForOptionCount(44); !strings.Contains(got, "↑↓") || strings.Contains(got, "10") {
		t.Fatalf("placeholder = %q", got)
	}
	if got := placeholderForOptionCount(3); got != "press 1, 2 or 3" {
		t.Fatalf("placeholder = %q", got)
	}
}
