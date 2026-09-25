package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/testkit/fauxtest"
)

const script = `
model: faux-1
steps:
  - text: "I'll look at the file."
  - tool_call: {name: read, args: {path: src/math.js}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "Fixed."
        usage: {input: 812, output: 34}
`

func startFaux(t *testing.T) string {
	t.Helper()
	addr, _ := fauxtest.Start(t, script)
	return "http://" + addr
}

func fauxModel(api provider.Api, baseURL string) provider.Model {
	return provider.Model{
		ID:            "faux-1",
		Name:          "faux-1",
		Api:           api,
		Provider:      "faux",
		BaseURL:       baseURL,
		Input:         []string{"text"},
		ContextWindow: 32768,
		MaxTokens:     4096,
	}
}

func collectEvents(ch <-chan msg.StreamEvent) []msg.StreamEvent {
	var out []msg.StreamEvent
	for e := range ch {
		out = append(out, e)
	}
	return out
}

func TestAnthropicClientFauxRoundTrip(t *testing.T) {
	baseURL := startFaux(t)
	model := fauxModel(provider.ApiAnthropicMessages, baseURL)
	client := &AnthropicClient{}

	transcript := []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("look at the math file")}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, transcript, provider.StreamOptions{}, Auth{APIKey: "test-key"})
	all := collectEvents(events)
	final, err := wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}

	assertTurn1(t, all, final)

	// Second turn: send the tool result back.
	toolCallID := msg.ToolCallsOf(final.Content)[0].ID
	transcript = append(transcript,
		msg.AssistantMessage{Role: msg.RoleAssistant, Content: final.Content},
		msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: toolCallID, ToolName: "read", Content: msg.Blocks{msg.Text("function add(a,b){return a-b}")}},
	)
	events2, wait2 := client.Stream(ctx, model, transcript, provider.StreamOptions{}, Auth{APIKey: "test-key"})
	all2 := collectEvents(events2)
	final2, err := wait2()
	if err != nil {
		t.Fatalf("wait2: %v", err)
	}
	if msg.TextOf(final2.Content) != "Fixed." {
		t.Fatalf("turn 2 text = %q, want %q", msg.TextOf(final2.Content), "Fixed.")
	}
	if final2.Usage.Input != 812 || final2.Usage.Output != 34 {
		t.Fatalf("turn 2 usage = %+v, want input=812 output=34", final2.Usage)
	}
	if final2.StopReason != msg.StopStop {
		t.Fatalf("turn 2 stop reason = %q, want stop", final2.StopReason)
	}
	_ = all2
}

func assertTurn1(t *testing.T, all []msg.StreamEvent, final *msg.AssistantMessage) {
	t.Helper()
	if len(all) == 0 {
		t.Fatal("no events received")
	}
	if all[0].Type != msg.EventStart {
		t.Fatalf("first event = %q, want start", all[0].Type)
	}
	last := all[len(all)-1]
	if last.Type != msg.EventDone {
		t.Fatalf("last event = %q, want done", last.Type)
	}
	if final.StopReason != msg.StopToolUse {
		t.Fatalf("stop reason = %q, want toolUse", final.StopReason)
	}
	text := msg.TextOf(final.Content)
	if text != "I'll look at the file." {
		t.Fatalf("text = %q", text)
	}
	calls := msg.ToolCallsOf(final.Content)
	if len(calls) != 1 || calls[0].Name != "read" {
		t.Fatalf("tool calls = %+v", calls)
	}
	if args, ok := calls[0].Arguments["path"]; !ok || args != "src/math.js" {
		t.Fatalf("tool call args = %+v", calls[0].Arguments)
	}

	sawTextDelta, sawToolCallDelta := false, false
	for _, e := range all {
		if e.Type == msg.EventTextDelta {
			sawTextDelta = true
		}
		if e.Type == msg.EventToolCallDelta {
			sawToolCallDelta = true
		}
	}
	if !sawTextDelta || !sawToolCallDelta {
		t.Fatalf("expected both text and toolcall deltas, sawText=%v sawToolCall=%v", sawTextDelta, sawToolCallDelta)
	}
}

func TestOpenAICompletionsClientFauxRoundTrip(t *testing.T) {
	baseURL := startFaux(t)
	model := fauxModel(provider.ApiOpenAICompletions, baseURL+"/v1")
	client := &OpenAICompletionsClient{}

	transcript := []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("look at the math file")}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, transcript, provider.StreamOptions{}, Auth{APIKey: "test-key"})
	all := collectEvents(events)
	final, err := wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if final.StopReason != msg.StopToolUse {
		t.Fatalf("stop reason = %q, want toolUse", final.StopReason)
	}
	if msg.TextOf(final.Content) != "I'll look at the file." {
		t.Fatalf("text = %q", msg.TextOf(final.Content))
	}
	calls := msg.ToolCallsOf(final.Content)
	if len(calls) != 1 || calls[0].Name != "read" {
		t.Fatalf("tool calls = %+v", calls)
	}
	sawStart := false
	for _, e := range all {
		if e.Type == msg.EventStart {
			sawStart = true
		}
	}
	if !sawStart {
		t.Fatal("missing start event")
	}
}

func TestAnthropicClientErrorStep(t *testing.T) {
	addr, _ := fauxtest.Start(t, `
model: faux-1
steps:
  - error: {status: 529, type: overloaded_error, message: "Overloaded"}
`)

	model := fauxModel(provider.ApiAnthropicMessages, "http://"+addr)
	client := &AnthropicClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}},
	}, provider.StreamOptions{}, Auth{APIKey: "test-key"})
	_ = collectEvents(events)
	_, err := wait()
	if err == nil {
		t.Fatal("expected an error for a 529 response")
	}
	var statusErr *StatusError
	if se, ok := asStatusError(err); ok {
		statusErr = se
	}
	if statusErr == nil {
		t.Fatalf("expected a *StatusError, got %T: %v", err, err)
	}
	if statusErr.Status != 529 || !statusErr.Retriable {
		t.Fatalf("status error = %+v, want status=529 retriable=true", statusErr)
	}
}

// TestAnthropicClientDisconnectMidStream drives the real AnthropicClient
// against a faux server that cuts the TCP connection mid-response
// (disconnect_after), the same fault test/e2e/resilience_test.go's
// TestResilience_StreamCutMidResponse exercises end to end. It asserts the
// provider layer's half of that e2e test's fix in isolation: the resulting
// error is a provider.StreamInterrupted (so internal/harness/retry.go's
// isRetriable treats it the same as a 5xx/529 response), wrapping the
// underlying read error (io.ErrUnexpectedEOF for a mid-chunk TCP cut).
func TestAnthropicClientDisconnectMidStream(t *testing.T) {
	addr, _ := fauxtest.Start(t, `
model: faux-1
steps:
  - text: "this reply gets cut short"
    disconnect_after: 20
`)
	model := fauxModel(provider.ApiAnthropicMessages, "http://"+addr)
	client := &AnthropicClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}},
	}, provider.StreamOptions{}, Auth{APIKey: "test-key"})
	_ = collectEvents(events)
	_, err := wait()
	if err == nil {
		t.Fatal("expected an error for a mid-stream disconnect")
	}
	var si provider.StreamInterrupted
	if !errors.As(err, &si) {
		t.Fatalf("expected a provider.StreamInterrupted, got %T: %v", err, err)
	}
	if si.Cause == nil {
		t.Fatalf("StreamInterrupted.Cause is nil, want the underlying read error (e.g. io.ErrUnexpectedEOF)")
	}
}

// TestAnthropicClientMalformedToolArgs drives the real AnthropicClient
// against a faux server scripting a tool call with raw_args set to
// unterminated JSON (`{"path": `), the same faux script
// test/e2e/resilience_test.go's TestResilience_MalformedToolArgs uses. It
// asserts the provider-visible half of that gap: the accumulated tool call
// ends with ToolCall.InvalidArgs set to the raw, unparsed text and
// Arguments left empty, rather than silently defaulting Arguments to {}
// with no signal that the model's tool call was malformed.
//
// The harness-side half (internal/harness/turn.go's beginTool must check
// call.InvalidArgs != "" and return an error tool_result instead of
// executing) is a separate, not-yet-made change; see this task's report.
func TestAnthropicClientMalformedToolArgs(t *testing.T) {
	addr, _ := fauxtest.Start(t, `
model: faux-1
steps:
  - tool_call: {name: bash, raw_args: '{"path": ', id: tc1}
`)
	model := fauxModel(provider.ApiAnthropicMessages, "http://"+addr)
	client := &AnthropicClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("run something")}},
	}, provider.StreamOptions{}, Auth{APIKey: "test-key"})
	_ = collectEvents(events)
	final, err := wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	calls := msg.ToolCallsOf(final.Content)
	if len(calls) != 1 || calls[0].Name != "bash" {
		t.Fatalf("tool calls = %+v", calls)
	}
	if calls[0].InvalidArgs == "" {
		t.Fatalf("tool call InvalidArgs is empty, want the raw unparsed args text")
	}
	if len(calls[0].Arguments) != 0 {
		t.Fatalf("tool call Arguments = %+v, want empty when InvalidArgs is set", calls[0].Arguments)
	}
}

func asStatusError(err error) (*StatusError, bool) {
	type unwrapper interface{ Unwrap() error }
	for e := err; e != nil; {
		if se, ok := e.(*StatusError); ok {
			return se, true
		}
		u, ok := e.(unwrapper)
		if !ok {
			break
		}
		e = u.Unwrap()
	}
	return nil, false
}
