package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/tools"
)

func typeText(p *PromptState, s string) {
	for _, r := range s {
		p.HandleKey(key(string(r)))
	}
}

func renderedText(p *PromptState) string {
	return stripANSI(strings.Join(p.Render(80), "\n"))
}

func TestTextEntry_TypesBackspacesAndEnterAnswers(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := p.AskText("Enter a name", TextEntry{})
	typeText(p, "abcd")
	p.HandleKey(backspaceKey())
	if !p.Paste("ef") {
		t.Fatal("the entry field did not take a paste")
	}
	p.HandleKey(key("enter"))
	r := <-reply
	if r.Cancelled || len(r.Answers) != 1 || r.Answers[0].Answers[0] != "abcef" {
		t.Fatalf("reply = %+v, want the one answer abcef", r)
	}
	if p.Active() {
		t.Fatal("the prompt stayed up after Enter")
	}
}

func TestTextEntry_OpensInTheFieldWithNoOptionList(t *testing.T) {
	p := NewPromptState("/tmp")
	p.AskText("Enter a name", TextEntry{})
	out := renderedText(p)
	if strings.Contains(out, "Other") || strings.Contains(out, "1.") {
		t.Fatalf("an option list shows in a bare text field:\n%s", out)
	}
	if !strings.Contains(out, "Enter a name") || !strings.Contains(out, "enter to send · esc to cancel") {
		t.Fatalf("question or hint missing:\n%s", out)
	}
}

func TestTextEntry_EmptyEnterStaysAndEscCancels(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := p.AskText("Enter a key", TextEntry{})
	p.HandleKey(key("enter"))
	if p.question.otherText == nil {
		t.Fatal("an empty Enter left the text field")
	}
	typeText(p, "   ")
	p.HandleKey(key("enter"))
	select {
	case r := <-reply:
		t.Fatalf("an empty Enter answered: %+v", r)
	default:
	}
	if !p.Active() || p.question.otherText == nil {
		t.Fatal("an empty Enter left the text field")
	}
	p.HandleKey(key("esc"))
	if r := <-reply; !r.Cancelled {
		t.Fatalf("reply = %+v, want cancelled", r)
	}
}

func TestTextEntry_AllowEmptyAnswersBlank(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := p.AskText("Domain (blank for the default)", TextEntry{AllowEmpty: true})
	p.HandleKey(key("enter"))
	r := <-reply
	if r.Cancelled || r.Answers[0].Answers[0] != "" {
		t.Fatalf("reply = %+v, want a blank answer", r)
	}
}

func TestTextEntry_PasteDropsNewlines(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := p.AskText("Enter a key", TextEntry{Secret: true})
	p.Paste("sk-abc\r\n")
	p.HandleKey(key("enter"))
	if got := (<-reply).Answers[0].Answers[0]; got != "sk-abc" {
		t.Fatalf("answer = %q, want sk-abc", got)
	}
}

func TestTextEntry_SecretIsMasked(t *testing.T) {
	const secret = "sk-test-SECRET-123"
	p := NewPromptState("/tmp")
	p.AskText("Enter API key", TextEntry{Secret: true, Placeholder: "must not show"})
	typeText(p, secret)
	out := renderedText(p)
	if strings.Contains(out, secret) || strings.Contains(out, "SECRET") || strings.Contains(out, "sk-") {
		t.Fatalf("the secret is on screen:\n%s", out)
	}
	if want := strings.Repeat("•", len(secret)); !strings.Contains(out, want) {
		t.Fatalf("want %d bullets in:\n%s", len(secret), out)
	}
	if strings.Contains(out, "must not show") {
		t.Fatal("a secret field drew its placeholder")
	}
}

func TestTextEntry_PlainFieldShowsPlaceholderUntilTyped(t *testing.T) {
	p := NewPromptState("/tmp")
	p.AskText("Domain", TextEntry{Placeholder: "company.ghe.com"})
	if !strings.Contains(renderedText(p), "company.ghe.com") {
		t.Fatal("placeholder not shown while empty")
	}
	typeText(p, "x")
	if strings.Contains(renderedText(p), "company.ghe.com") {
		t.Fatal("placeholder still shown after typing")
	}
}

func TestTextEntry_WithdrawRemovesIt(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := p.AskText("Enter a key", TextEntry{Secret: true})
	typeText(p, "abc")
	p.withdraw(reply)
	if p.Active() {
		t.Fatal("a withdrawn text entry stayed on screen")
	}
}

func TestNoOtherDropsTheOtherRow(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := make(chan AskUserReply, 1)
	p.enqueue(queuedPrompt{question: newPendingQuestion([]tools.AskUserQuestion{{
		Question: "Which?",
		Options:  []tools.AskUserOption{{Label: "a"}, {Label: "b"}},
	}}, nil, true, reply)})
	if out := renderedText(p); strings.Contains(out, "Other") {
		t.Fatalf("a closed choice offers Other:\n%s", out)
	}
	p.HandleKey(key("down"))
	p.HandleKey(key("down"))
	p.HandleKey(key("3"))
	select {
	case r := <-reply:
		t.Fatalf("a row past the options answered: %+v", r)
	default:
	}
	p.HandleKey(key("enter"))
	select {
	case r := <-reply:
		if r.Answers[0].Answers[0] != "b" {
			t.Fatalf("answer = %+v, want b (the last row)", r)
		}
	default:
		t.Fatal("Enter on the last row did not answer")
	}
}

// Through the Model: the typed secret reaches the reply channel only. It is
// not in the editor, the rendered view, or the transcript, before or after
// Enter.
func TestTextEntry_SecretNeverLeavesTheReply(t *testing.T) {
	const secret = "sk-live-NEVERSHOW"
	m, sink := newTestModelWithBridge(t)
	reply := make(chan AskUserReply, 1)
	m = updateModel(t, m, MsgAskUserPrompt{
		Questions: []tools.AskUserQuestion{{Question: "Enter API key"}},
		Entry:     &TextEntry{Secret: true},
		Reply:     reply,
	})
	for _, r := range secret {
		m = updateModel(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if v := m.editor.Value(); v != "" {
		t.Fatalf("the secret leaked into the editor: %q", v)
	}
	if strings.Contains(stripANSI(m.View().Content), secret) {
		t.Fatal("the secret is in the rendered view")
	}
	m = updateModel(t, m, key("enter"))
	select {
	case r := <-reply:
		if r.Answers[0].Answers[0] != secret {
			t.Fatalf("answer = %q", r.Answers[0].Answers[0])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no reply")
	}
	if strings.Contains(stripANSI(m.View().Content), secret) {
		t.Fatal("the secret is in the rendered view after Enter")
	}
	printed, _ := sink.snapshot()
	for _, p := range printed {
		if strings.Contains(p, secret) {
			t.Fatal("the secret reached the transcript")
		}
	}
}
