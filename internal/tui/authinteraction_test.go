package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/auth"
	"github.com/andrepato/harness/internal/tools"
)

// authHarness runs one Prompt on its own goroutine, as login does, and
// hands the test the prompt message the bridge sent the program.
type authHarness struct {
	b    *Bridge
	sink *recordSink
	done chan promptResult
}

type promptResult struct {
	val string
	err error
}

func startPrompt(t *testing.T, ctx context.Context, p auth.Prompt) *authHarness {
	t.Helper()
	b := NewBridge("/tmp")
	t.Cleanup(b.Stop)
	sink := &recordSink{}
	b.setSink(sink)
	h := &authHarness{b: b, sink: sink, done: make(chan promptResult, 1)}
	ia := NewAuthInteraction(b)
	go func() {
		v, err := ia.Prompt(ctx, p)
		h.done <- promptResult{v, err}
	}()
	return h
}

func (h *authHarness) prompt(t *testing.T) MsgAskUserPrompt {
	t.Helper()
	waitFor(t, func() bool { return len(h.sink.snapshot()) >= 1 })
	m, ok := h.sink.snapshot()[0].(MsgAskUserPrompt)
	if !ok {
		t.Fatalf("first message = %T, want MsgAskUserPrompt", h.sink.snapshot()[0])
	}
	return m
}

func (h *authHarness) result(t *testing.T) promptResult {
	t.Helper()
	select {
	case r := <-h.done:
		return r
	case <-time.After(30 * time.Second):
		t.Fatal("Prompt did not return")
		return promptResult{}
	}
}

func TestAuthInteraction_SelectMapsLabelToID(t *testing.T) {
	h := startPrompt(t, context.Background(), auth.Prompt{
		Type:    auth.PromptSelect,
		Message: "How do you want to sign in?",
		Options: []auth.SelectOption{
			{ID: "subscription", Label: "ChatGPT subscription"},
			{ID: "api_key", Label: "OpenAI API key", Description: "paste a key"},
		},
	})
	m := h.prompt(t)
	q := m.Questions[0]
	if q.Question != "How do you want to sign in?" || len(q.Options) != 2 || q.Options[1].Label != "OpenAI API key" {
		t.Fatalf("question = %+v", q)
	}
	if m.Entry != nil || !m.NoOther {
		t.Fatalf("a select must be a closed option list: entry=%v noOther=%v", m.Entry, m.NoOther)
	}
	m.Reply <- AskUserReply{Answers: []tools.AskUserAnswer{{Question: q.Question, Answers: []string{"OpenAI API key"}}}}
	if r := h.result(t); r.err != nil || r.val != "api_key" {
		t.Fatalf("result = %+v, want api_key", r)
	}
}

func TestAuthInteraction_TextKindsReturnTheTypedValue(t *testing.T) {
	cases := []struct {
		kind       auth.PromptKind
		secret     bool
		allowEmpty bool
	}{
		{auth.PromptSecret, true, false},
		{auth.PromptText, false, true},
		{auth.PromptManualCode, false, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			h := startPrompt(t, context.Background(), auth.Prompt{Type: tc.kind, Message: "Enter it", Placeholder: "ph"})
			m := h.prompt(t)
			if m.Entry == nil || m.Entry.Secret != tc.secret || m.Entry.AllowEmpty != tc.allowEmpty || m.Entry.Placeholder != "ph" {
				t.Fatalf("entry = %+v", m.Entry)
			}
			m.Reply <- AskUserReply{Answers: []tools.AskUserAnswer{{Answers: []string{"typed-value"}}}}
			if r := h.result(t); r.err != nil || r.val != "typed-value" {
				t.Fatalf("result = %+v", r)
			}
		})
	}
}

func TestAuthInteraction_CancelIsAnError(t *testing.T) {
	h := startPrompt(t, context.Background(), auth.Prompt{Type: auth.PromptSecret, Message: "Key"})
	h.prompt(t).Reply <- AskUserReply{Cancelled: true}
	if r := h.result(t); r.err == nil || r.val != "" {
		t.Fatalf("result = %+v, want an error", r)
	}
}

func TestAuthInteraction_CtxCancelWithdrawsThePrompt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h := startPrompt(t, ctx, auth.Prompt{Type: auth.PromptManualCode, Message: "Paste the code"})
	m := h.prompt(t)
	cancel()
	if r := h.result(t); !errors.Is(r.err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", r.err)
	}
	waitFor(t, func() bool { return len(h.sink.snapshot()) >= 2 })
	w, ok := h.sink.snapshot()[1].(MsgPromptWithdrawn)
	if !ok || w.Reply != m.Reply {
		t.Fatalf("second message = %#v, want the withdrawal of the prompt's own reply channel", h.sink.snapshot()[1])
	}
}

func TestAuthInteraction_NotifyCommitsToTheTranscript(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "test") // headless: never open a real browser
	b := NewBridge("/tmp")
	t.Cleanup(b.Stop)
	f := &fakeSink{}
	b.setSink(f)
	ia := NewAuthInteraction(b)
	ia.Notify(auth.Event{Type: auth.EventInfo, Message: "Logged in to openrouter."})
	ia.Notify(auth.Event{Type: auth.EventAuthURL, URL: "https://example.test/authorize?x=1", Instructions: "Open this to sign in:"})
	ia.Notify(auth.Event{Type: auth.EventDeviceCode, UserCode: "ABCD-1234", VerificationURI: "https://example.test/device", ExpiresInSeconds: 900})
	for _, want := range []string{
		"Logged in to openrouter.",
		"Open this to sign in:",
		"https://example.test/authorize?x=1",
		"ABCD-1234",
		"https://example.test/device",
		"expires in 15 min",
	} {
		waitForPrinted(t, f, want)
	}
	printed, _ := f.snapshot()
	if strings.Contains(strings.Join(printed, "\n"), "Login failed") {
		t.Fatal("unexpected text")
	}
}
