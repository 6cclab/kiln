package router

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

var (
	heavy  = provider.Model{Provider: "cloud", ID: "heavy", Reasoning: true, ContextWindow: 200000, MaxTokens: 32000, Input: []string{"text", "image"}}
	mid    = provider.Model{Provider: "cloud", ID: "mid", Reasoning: true, ContextWindow: 200000, MaxTokens: 64000, Input: []string{"text", "image"}}
	cheap  = provider.Model{Provider: "local", ID: "cheap", ContextWindow: 32768, MaxTokens: 8192, Input: []string{"text"}}
	direct = provider.Model{Provider: "local", ID: "direct", ContextWindow: 65536, MaxTokens: 4096, Input: []string{"text"}}
	autoT  = Targets{Plan: mid, PlanComplex: heavy, Implement: cheap, Direct: direct}
)

func user(text string) msg.UserMessage {
	return msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.TextContent{Type: "text", Text: text}}}
}

func toolResult(name string, isError bool) msg.ToolResultMessage {
	return msg.ToolResultMessage{Role: msg.RoleToolResult, ToolName: name, IsError: isError}
}

func state(t *testing.T, phase string, m provider.Model) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(AutoState{Phase: phase, Model: modelKey(m)})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func decode(t *testing.T, raw json.RawMessage) *AutoState {
	t.Helper()
	if raw == nil {
		return nil
	}
	var s AutoState
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return &s
}

// stubClassifier answers with c and err, and records the prompts it read.
type stubClassifier struct {
	c       Complexity
	err     error
	prompts []string
}

func (s *stubClassifier) Classify(_ context.Context, prompt string) (Complexity, error) {
	s.prompts = append(s.prompts, prompt)
	return s.c, s.err
}

// Each of jev's rules, in order.
func TestAutoRoutesAsJev(t *testing.T) {
	cases := []struct {
		name      string
		auto      Auto
		req       Request
		wantModel provider.Model
		wantState *AutoState
	}{
		{"direct goes to the direct slot, with no state",
			Auto{Targets: autoT}, Request{Reason: ReasonDirect, State: state(t, PhasePlanning, mid)},
			direct, nil},
		{"no state, no classifier: plan",
			Auto{Targets: autoT}, Request{Reason: ReasonUser, Messages: []msg.Message{user("fix it")}},
			mid, &AutoState{PhasePlanning, "cloud/mid"}},
		{"no state, previous is the planComplex model: keep it",
			Auto{Targets: autoT}, Request{Reason: ReasonUser, Previous: &Choice{Model: heavy}},
			heavy, &AutoState{PhasePlanning, "cloud/heavy"}},
		{"no state, previous is the plan model: keep it without classifying",
			Auto{Targets: autoT, Classifier: &stubClassifier{c: Complex}}, Request{Reason: ReasonUser, Previous: &Choice{Model: mid}},
			mid, &AutoState{PhasePlanning, "cloud/mid"}},
		{"no state, previous is another model: classify",
			Auto{Targets: autoT, Classifier: &stubClassifier{c: Complex}}, Request{Reason: ReasonUser, Previous: &Choice{Model: cheap}},
			heavy, &AutoState{PhasePlanning, "cloud/heavy"}},
		{"no state, classifier says standard: plan",
			Auto{Targets: autoT, Classifier: &stubClassifier{c: Standard}}, Request{Reason: ReasonUser},
			mid, &AutoState{PhasePlanning, "cloud/mid"}},
		{"no state, classifier fails: plan",
			Auto{Targets: autoT, Classifier: &stubClassifier{err: errors.New("down")}}, Request{Reason: ReasonUser},
			mid, &AutoState{PhasePlanning, "cloud/mid"}},
		{"no state, classifier gives no usable answer: plan",
			Auto{Targets: autoT, Classifier: &stubClassifier{c: "maybe"}}, Request{Reason: ReasonUser},
			mid, &AutoState{PhasePlanning, "cloud/mid"}},
		{"no state, classifier fails, fallback says complex",
			Auto{Targets: autoT, Classifier: &stubClassifier{err: errors.New("down")}, Fallback: func(string) Complexity { return Complex }},
			Request{Reason: ReasonUser}, heavy, &AutoState{PhasePlanning, "cloud/heavy"}},
		{"unreadable state counts as none",
			Auto{Targets: autoT}, Request{Reason: ReasonContinuation, State: json.RawMessage(`{"phase":"later"}`)},
			mid, &AutoState{PhasePlanning, "cloud/mid"}},
		{"planning, an edit succeeded since the user's message: implement",
			Auto{Targets: autoT}, Request{Reason: ReasonContinuation, State: state(t, PhasePlanning, heavy),
				Messages: []msg.Message{user("go"), msg.AssistantMessage{}, toolResult("edit", false)}},
			cheap, &AutoState{PhaseImplementation, "local/cheap"}},
		{"planning, a write succeeded: implement",
			Auto{Targets: autoT}, Request{Reason: ReasonRetry, State: state(t, PhasePlanning, mid),
				Messages: []msg.Message{user("go"), toolResult("read", false), toolResult("write", false)}},
			cheap, &AutoState{PhaseImplementation, "local/cheap"}},
		{"planning, the edit failed: stay",
			Auto{Targets: autoT}, Request{Reason: ReasonContinuation, State: state(t, PhasePlanning, heavy),
				Messages: []msg.Message{user("go"), toolResult("edit", true), toolResult("bash", false)}},
			heavy, nil},
		{"planning, the edit was before the user's last message: stay",
			Auto{Targets: autoT}, Request{Reason: ReasonUser, State: state(t, PhasePlanning, heavy),
				Messages: []msg.Message{user("go"), toolResult("edit", false), user("now this")}},
			heavy, nil},
		{"implementation stays, even on a new user message",
			Auto{Targets: autoT, Classifier: &stubClassifier{c: Complex}}, Request{Reason: ReasonUser, State: state(t, PhaseImplementation, cheap),
				Messages: []msg.Message{user("a hard new problem")}},
			cheap, nil},
		{"stale planning model: re-derived to plan and stored",
			Auto{Targets: autoT}, Request{Reason: ReasonContinuation, State: json.RawMessage(`{"phase":"planning","model":"gone/old"}`)},
			mid, &AutoState{PhasePlanning, "cloud/mid"}},
		{"stale implementation model: re-derived to implement and stored",
			Auto{Targets: autoT}, Request{Reason: ReasonContinuation, State: json.RawMessage(`{"phase":"implementation","model":"gone/old"}`)},
			cheap, &AutoState{PhaseImplementation, "local/cheap"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.req.ThinkingLevel = provider.ThinkingHigh
			got, err := c.auto.Route(context.Background(), c.req)
			if err != nil {
				t.Fatal(err)
			}
			if modelKey(got.Model) != modelKey(c.wantModel) {
				t.Errorf("model = %s, want %s", modelKey(got.Model), modelKey(c.wantModel))
			}
			if got.ThinkingLevel != provider.ThinkingHigh {
				t.Errorf("thinking = %q, want the request's high passed through", got.ThinkingLevel)
			}
			if gotState := decode(t, got.State); !reflect.DeepEqual(gotState, c.wantState) {
				t.Errorf("state = %+v, want %+v", gotState, c.wantState)
			}
		})
	}
}

// The classifier reads the last user message's text, clipped to 16,000
// characters, as jev's lastUserText(...).slice(0, 16_000).
func TestAutoClassifierReadsTheLastUserText(t *testing.T) {
	cl := &stubClassifier{c: Standard}
	a := Auto{Targets: autoT, Classifier: cl}
	long := strings.Repeat("é", classifierPromptChars+50)
	if _, err := a.Route(context.Background(), Request{Reason: ReasonUser, Messages: []msg.Message{user("first"), msg.AssistantMessage{}, user(long)}}); err != nil {
		t.Fatal(err)
	}
	if len(cl.prompts) != 1 || len([]rune(cl.prompts[0])) != classifierPromptChars || !strings.HasPrefix(cl.prompts[0], "éé") {
		t.Fatalf("classifier read %d prompts, first %d runes", len(cl.prompts), len([]rune(cl.prompts[0])))
	}
}

// The classifier reads what the user typed (KilnTyped), not the content
// kiln built around it, so an @file expansion never reaches it.
func TestAutoClassifierReadsTypedTextNotContent(t *testing.T) {
	cl := &stubClassifier{c: Standard}
	a := Auto{Targets: autoT, Classifier: cl}
	typed := msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.TextContent{Type: "text", Text: "fix @foo.go\n\n<attached contents of foo.go, 400 lines>"}}, KilnTyped: "fix @foo.go"}
	if _, err := a.Route(context.Background(), Request{Reason: ReasonUser, Messages: []msg.Message{typed}}); err != nil {
		t.Fatal(err)
	}
	if len(cl.prompts) != 1 || cl.prompts[0] != "fix @foo.go" {
		t.Fatalf("classifier read %q, want the typed line only", cl.prompts)
	}
}

// A message with no KilnTyped (a subagent's delegated task, or one
// written before the field existed) falls back to its content.
func TestAutoClassifierFallsBackToContentWithoutKilnTyped(t *testing.T) {
	cl := &stubClassifier{c: Standard}
	a := Auto{Targets: autoT, Classifier: cl}
	if _, err := a.Route(context.Background(), Request{Reason: ReasonUser, Messages: []msg.Message{user("plain content")}}); err != nil {
		t.Fatal(err)
	}
	if len(cl.prompts) != 1 || cl.prompts[0] != "plain content" {
		t.Fatalf("classifier read %q, want the message's content", cl.prompts)
	}
}

// Esc during classification (ctx cancelled) must abort the turn: Route
// returns the cancellation error, never a fallback choice, even when a
// Fallback is configured.
func TestAutoRouteAbortsOnCtxCancelDuringClassification(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cl := &cancelingClassifier{cancel: cancel}
	a := Auto{Targets: autoT, Classifier: cl, Fallback: func(string) Complexity { return Complex }}
	_, err := a.Route(ctx, Request{Reason: ReasonUser, Messages: []msg.Message{user("go")}})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// cancelingClassifier cancels ctx itself (simulating Esc firing while the
// call is in flight) and returns an error, as a provider call aborted by
// ctx cancellation would.
type cancelingClassifier struct {
	cancel context.CancelFunc
}

func (c *cancelingClassifier) Classify(ctx context.Context, prompt string) (Complexity, error) {
	c.cancel()
	return "", context.Canceled
}

// kiln/auto's limits fit every target: the smallest window and output
// cap, the inputs all of them take, and reasoning if any reasons.
func TestAutoModel(t *testing.T) {
	m := AutoModel(autoT)
	if m.Provider != "kiln" || m.ID != "auto" || m.Name != "Auto (kiln)" || !provider.IsVirtual(m) {
		t.Errorf("identity = %+v", m)
	}
	// The Plan target's limits, not the smallest target's (cheap's
	// 32768/direct's 4096): the session starts sized for the model its
	// first request goes to and re-sizes on each route.
	if m.ContextWindow != 200000 || m.MaxTokens != 64000 {
		t.Errorf("window/max = %d/%d, want plan's 200000/64000", m.ContextWindow, m.MaxTokens)
	}
	if !reflect.DeepEqual(m.Input, []string{"text"}) {
		t.Errorf("input = %v, want [text]", m.Input)
	}
	if !m.Reasoning {
		t.Error("reasoning = false, want true (heavy and mid reason)")
	}
	none := AutoModel(Targets{Plan: cheap, PlanComplex: cheap, Implement: direct, Direct: direct})
	if none.Reasoning {
		t.Error("reasoning = true with no reasoning target")
	}
}

// The kiln provider never streams.
func TestVirtualProviderRefusesToStream(t *testing.T) {
	p := NewVirtualProvider(AutoModel(autoT))
	if p.Auth().Kind != provider.AuthKindNone || len(p.Models()) != 1 {
		t.Fatalf("provider = %+v", p)
	}
	ch, wait := p.Stream(context.Background(), p.Models()[0], nil, provider.StreamOptions{})
	for range ch {
	}
	if _, err := wait(); err == nil || !strings.Contains(err.Error(), "must be routed") {
		t.Fatalf("Stream error = %v", err)
	}
}
