package api

// Conformance tests replayed against recorded/hand-authored SSE fixtures
// (testdata/{anthropic,openai}/*.sse) rather than a live faux server, so the
// suite is fast and deterministic. TestAnthropicClientFauxRoundTrip /
// TestOpenAICompletionsClientFauxRoundTrip in conformance_test.go keep the
// live-faux round trip as a canary that the recorder and faux itself still
// agree with the client's expectations.
//
// Fixture provenance:
//   - text_only, thinking_text, tool_call_partial, error_429, error_529:
//     recorded from a live faux server (see recordFixtures* below). Rerecord
//     with `go test ./internal/provider/api/... -run Fixture -record`.
//   - multi_tool_calls: hand-authored. faux's script engine flushes a turn
//     as soon as it sees a tool_call step (internal/testkit/faux/script.go's
//     flattenSteps), so it cannot produce two tool_use/tool_calls blocks in
//     one assistant message; there is no live source to record this from.
//   - malformed_truncated: hand-authored by cutting a real stream off
//     mid-event (no message_delta/message_stop, and no trailing blank line
//     on the last data line), to exercise scanSSE returning io.EOF before a
//     stop reason ever arrived.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

func costedModel(api provider.Api, baseURL string) provider.Model {
	return provider.Model{
		ID:            "faux-1",
		Name:          "faux-1",
		Api:           api,
		Provider:      "faux",
		BaseURL:       baseURL,
		Input:         []string{"text"},
		ContextWindow: 32768,
		MaxTokens:     4096,
		Cost: provider.ModelCost{
			ModelCostRates: provider.ModelCostRates{
				Input:      3,
				Output:     15,
				CacheRead:  0.3,
				CacheWrite: 3.75,
			},
		},
	}
}

// --- shared assertions ----------------------------------------------------

// assertPartialIdentity asserts every event's Partial pointer is the same
// *msg.AssistantMessage across the whole stream, and that it is the same
// object handed back as the final message (or error).
func assertPartialIdentity(t *testing.T, all []msg.StreamEvent, final *msg.AssistantMessage) {
	t.Helper()
	var want *msg.AssistantMessage
	for _, e := range all {
		if e.Partial == nil {
			continue
		}
		if want == nil {
			want = e.Partial
		} else if e.Partial != want {
			t.Fatalf("event %s carried a different Partial pointer (%p vs %p): Partial must mutate in place, not be replaced", e.Type, e.Partial, want)
		}
	}
	if final != nil && want != nil && final != want {
		t.Fatalf("final message pointer (%p) differs from the Partial pointer streamed throughout (%p)", final, want)
	}
}

// assertTextMonotonic asserts every EventTextDelta for contentIndex grows
// the text already recorded on Partial -- it never shrinks or resets.
func assertTextMonotonic(t *testing.T, all []msg.StreamEvent) {
	t.Helper()
	lastLen := map[int]int{}
	for _, e := range all {
		if e.Type != msg.EventTextDelta {
			continue
		}
		tc, ok := e.Partial.Content[e.ContentIndex].(msg.TextContent)
		if !ok {
			continue
		}
		if got, want := len(tc.Text), lastLen[e.ContentIndex]; got < want {
			t.Fatalf("text at index %d shrank from %d to %d chars after a delta", e.ContentIndex, want, got)
		}
		lastLen[e.ContentIndex] = len(tc.Text)
	}
}

func assertCost(t *testing.T, model provider.Model, u msg.Usage) {
	t.Helper()
	wantInput := float64(u.Input) / 1_000_000 * model.Cost.Input
	wantOutput := float64(u.Output) / 1_000_000 * model.Cost.Output
	wantCacheRead := float64(u.CacheRead) / 1_000_000 * model.Cost.CacheRead
	wantCacheWrite := float64(u.CacheWrite) / 1_000_000 * model.Cost.CacheWrite
	wantTotal := wantInput + wantOutput + wantCacheRead + wantCacheWrite
	if u.Cost.Total != wantTotal {
		t.Fatalf("usage.Cost.Total = %v, want %v (derived from model.Cost and usage: input=%d output=%d cacheRead=%d cacheWrite=%d)",
			u.Cost.Total, wantTotal, u.Input, u.Output, u.CacheRead, u.CacheWrite)
	}
}

// ============================================================================
// Anthropic
// ============================================================================

func TestFixtureAnthropicTextOnly(t *testing.T) {
	body := loadFixture(t, "anthropic", "text_only", func() []byte {
		return recordAnthropicFixture(t, `
model: faux-1
steps:
  - text: "Hello from the model."
    usage: {input: 120, output: 40}
`, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{})
	})

	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := costedModel(provider.ApiAnthropicMessages, srv.URL)
	client := &AnthropicClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{}, Auth{APIKey: "k"})
	all := collectEvents(events)
	final, err := wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}

	assertPartialIdentity(t, all, final)
	assertTextMonotonic(t, all)

	if msg.TextOf(final.Content) != "Hello from the model." {
		t.Fatalf("text = %q", msg.TextOf(final.Content))
	}
	if final.StopReason != msg.StopStop {
		t.Fatalf("StopReason = %q, want stop", final.StopReason)
	}
	if final.RawStopReason != "end_turn" {
		t.Fatalf("RawStopReason = %q, want end_turn", final.RawStopReason)
	}
	if final.ResponseID == "" {
		t.Fatal("ResponseID is empty")
	}
	if final.Usage.Input != 120 || final.Usage.Output != 40 {
		t.Fatalf("usage = %+v, want input=120 output=40 (usage arrived on the final message_delta chunk)", final.Usage)
	}
	assertCost(t, model, final.Usage)
}

func TestFixtureAnthropicThinkingText(t *testing.T) {
	body := loadFixture(t, "anthropic", "thinking_text", func() []byte {
		return recordAnthropicFixture(t, `
model: faux-1
steps:
  - thinking: "Reasoning about the question."
  - text: "Here is the answer."
    usage: {input: 200, output: 80}
`, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{})
	})

	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := costedModel(provider.ApiAnthropicMessages, srv.URL)
	client := &AnthropicClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{}, Auth{APIKey: "k"})
	all := collectEvents(events)
	final, err := wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	assertPartialIdentity(t, all, final)
	assertTextMonotonic(t, all)

	if len(final.Content) != 2 {
		t.Fatalf("content blocks = %d, want 2 (thinking, text)", len(final.Content))
	}
	thinking, ok := final.Content[0].(msg.ThinkingContent)
	if !ok || thinking.Thinking != "Reasoning about the question." {
		t.Fatalf("content[0] = %+v, want thinking %q", final.Content[0], "Reasoning about the question.")
	}
	if msg.TextOf(final.Content) != "Here is the answer." {
		t.Fatalf("text = %q", msg.TextOf(final.Content))
	}
	if final.StopReason != msg.StopStop {
		t.Fatalf("StopReason = %q, want stop", final.StopReason)
	}
	assertCost(t, model, final.Usage)
}

func TestFixtureAnthropicToolCallPartialJSON(t *testing.T) {
	body := loadFixture(t, "anthropic", "tool_call_partial", func() []byte {
		return recordAnthropicFixture(t, `
model: faux-1
steps:
  - text: "Let me check that."
  - tool_call: {name: search, args: {query: "weather in san francisco tomorrow", limit: 5}, id: tc1}
`, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("check the weather")}}}, provider.StreamOptions{})
	})

	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := costedModel(provider.ApiAnthropicMessages, srv.URL)
	client := &AnthropicClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("check the weather")}}}, provider.StreamOptions{}, Auth{APIKey: "k"})
	all := collectEvents(events)
	final, err := wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	assertPartialIdentity(t, all, final)

	deltaCount := 0
	for _, e := range all {
		if e.Type == msg.EventToolCallDelta {
			deltaCount++
		}
	}
	if deltaCount < 2 {
		t.Fatalf("tool call delta events = %d, want >=2 (partial JSON should arrive over multiple chunks)", deltaCount)
	}

	calls := msg.ToolCallsOf(final.Content)
	if len(calls) != 1 || calls[0].Name != "search" {
		t.Fatalf("tool calls = %+v", calls)
	}
	if calls[0].Arguments["query"] != "weather in san francisco tomorrow" || calls[0].Arguments["limit"] != float64(5) {
		t.Fatalf("tool call args = %+v", calls[0].Arguments)
	}
	if final.StopReason != msg.StopToolUse {
		t.Fatalf("StopReason = %q, want toolUse", final.StopReason)
	}
	if final.RawStopReason != "tool_use" {
		t.Fatalf("RawStopReason = %q, want tool_use", final.RawStopReason)
	}
}

func TestFixtureAnthropicMultiToolCalls(t *testing.T) {
	body := loadFixture(t, "anthropic", "multi_tool_calls", nil) // hand-authored; see file header
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := costedModel(provider.ApiAnthropicMessages, srv.URL)
	client := &AnthropicClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{}, Auth{APIKey: "k"})
	all := collectEvents(events)
	final, err := wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	assertPartialIdentity(t, all, final)

	calls := msg.ToolCallsOf(final.Content)
	if len(calls) != 2 {
		t.Fatalf("tool calls = %d, want 2", len(calls))
	}
	if calls[0].Name != "read_file" || calls[1].Name != "list_files" {
		t.Fatalf("tool call names = %q, %q", calls[0].Name, calls[1].Name)
	}
	if final.StopReason != msg.StopToolUse {
		t.Fatalf("StopReason = %q, want toolUse", final.StopReason)
	}
}

func TestFixtureAnthropicMalformedTruncated(t *testing.T) {
	body := loadFixture(t, "anthropic", "malformed_truncated", nil) // hand-authored; see file header
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := costedModel(provider.ApiAnthropicMessages, srv.URL)
	client := &AnthropicClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{}, Auth{APIKey: "k"})
	all := collectEvents(events)
	_, err := wait()
	if err == nil {
		t.Fatal("expected an error for a stream that ends mid-event with no stop reason")
	}
	last := all[len(all)-1]
	if last.Type != msg.EventError {
		t.Fatalf("last event = %q, want error", last.Type)
	}
	if last.Reason != msg.StopError {
		t.Fatalf("error reason = %q, want error", last.Reason)
	}
}

func TestFixtureAnthropicError429(t *testing.T) {
	testAnthropicErrorFixture(t, "error_429", 429, "rate_limit_error", true)
}

func TestFixtureAnthropicError529(t *testing.T) {
	testAnthropicErrorFixture(t, "error_529", 529, "overloaded_error", true)
}

func testAnthropicErrorFixture(t *testing.T, name string, status int, errType string, wantRetriable bool) {
	t.Helper()
	body := loadFixture(t, "anthropic", name, func() []byte {
		s, err := recordFauxErrorBody(t, "anthropic", status, errType, "synthetic error for conformance testing")
		if err != nil {
			t.Fatal(err)
		}
		return s
	})

	srv := replaySSE(t, status, "application/json", body)
	model := costedModel(provider.ApiAnthropicMessages, srv.URL)
	client := &AnthropicClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{}, Auth{APIKey: "k"})
	_ = collectEvents(events)
	_, err := wait()
	if err == nil {
		t.Fatalf("expected an error for a %d response", status)
	}
	se, ok := asStatusError(err)
	if !ok {
		t.Fatalf("expected a *StatusError, got %T: %v", err, err)
	}
	if se.Status != status || se.Retriable != wantRetriable {
		t.Fatalf("status error = %+v, want status=%d retriable=%v", se, status, wantRetriable)
	}
	if !strings.Contains(se.Body, errType) {
		t.Fatalf("status error body = %q, want it to contain %q", se.Body, errType)
	}
}

// ============================================================================
// OpenAI completions
// ============================================================================

func TestFixtureOpenAITextOnly(t *testing.T) {
	body := loadFixture(t, "openai", "text_only", func() []byte {
		return recordOpenAIFixture(t, `
model: faux-1
steps:
  - text: "Hello from the model."
    usage: {input: 120, output: 40}
`, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{})
	})

	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := costedModel(provider.ApiOpenAICompletions, srv.URL+"/v1")
	client := &OpenAICompletionsClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{}, Auth{APIKey: "k"})
	all := collectEvents(events)
	final, err := wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	assertPartialIdentity(t, all, final)
	assertTextMonotonic(t, all)

	if msg.TextOf(final.Content) != "Hello from the model." {
		t.Fatalf("text = %q", msg.TextOf(final.Content))
	}
	if final.StopReason != msg.StopStop {
		t.Fatalf("StopReason = %q, want stop", final.StopReason)
	}
	if final.RawStopReason != "stop" {
		t.Fatalf("RawStopReason = %q, want stop", final.RawStopReason)
	}
	if final.Usage.Input != 120 || final.Usage.Output != 40 {
		t.Fatalf("usage = %+v, want input=120 output=40 (usage arrived on the final chunk)", final.Usage)
	}
	assertCost(t, model, final.Usage)
}

func TestFixtureOpenAIThinkingText(t *testing.T) {
	body := loadFixture(t, "openai", "thinking_text", func() []byte {
		return recordOpenAIFixture(t, `
model: faux-1
steps:
  - thinking: "Reasoning about the question."
  - text: "Here is the answer."
    usage: {input: 200, output: 80}
`, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{})
	})

	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := costedModel(provider.ApiOpenAICompletions, srv.URL+"/v1")
	client := &OpenAICompletionsClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{}, Auth{APIKey: "k"})
	all := collectEvents(events)
	final, err := wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	assertPartialIdentity(t, all, final)

	if len(final.Content) != 2 {
		t.Fatalf("content blocks = %d, want 2 (thinking, text)", len(final.Content))
	}
	thinking, ok := final.Content[0].(msg.ThinkingContent)
	if !ok || thinking.Thinking != "Reasoning about the question." {
		t.Fatalf("content[0] = %+v, want thinking %q", final.Content[0], "Reasoning about the question.")
	}
	if msg.TextOf(final.Content) != "Here is the answer." {
		t.Fatalf("text = %q", msg.TextOf(final.Content))
	}
}

func TestFixtureOpenAIToolCallPartialJSON(t *testing.T) {
	body := loadFixture(t, "openai", "tool_call_partial", func() []byte {
		return recordOpenAIFixture(t, `
model: faux-1
steps:
  - text: "Let me check that."
  - tool_call: {name: search, args: {query: "weather in san francisco tomorrow", limit: 5}, id: tc1}
`, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("check the weather")}}}, provider.StreamOptions{})
	})

	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := costedModel(provider.ApiOpenAICompletions, srv.URL+"/v1")
	client := &OpenAICompletionsClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("check the weather")}}}, provider.StreamOptions{}, Auth{APIKey: "k"})
	all := collectEvents(events)
	final, err := wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	assertPartialIdentity(t, all, final)

	deltaCount := 0
	for _, e := range all {
		if e.Type == msg.EventToolCallDelta {
			deltaCount++
		}
	}
	if deltaCount < 2 {
		t.Fatalf("tool call delta events = %d, want >=2", deltaCount)
	}
	calls := msg.ToolCallsOf(final.Content)
	if len(calls) != 1 || calls[0].Name != "search" {
		t.Fatalf("tool calls = %+v", calls)
	}
	if final.StopReason != msg.StopToolUse {
		t.Fatalf("StopReason = %q, want toolUse", final.StopReason)
	}
	if final.RawStopReason != "tool_calls" {
		t.Fatalf("RawStopReason = %q, want tool_calls", final.RawStopReason)
	}
}

func TestFixtureOpenAIMultiToolCalls(t *testing.T) {
	body := loadFixture(t, "openai", "multi_tool_calls", nil) // hand-authored; see file header
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := costedModel(provider.ApiOpenAICompletions, srv.URL+"/v1")
	client := &OpenAICompletionsClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{}, Auth{APIKey: "k"})
	all := collectEvents(events)
	final, err := wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	assertPartialIdentity(t, all, final)

	calls := msg.ToolCallsOf(final.Content)
	if len(calls) != 2 {
		t.Fatalf("tool calls = %d, want 2", len(calls))
	}
	if calls[0].Name != "read_file" || calls[1].Name != "list_files" {
		t.Fatalf("tool call names = %q, %q", calls[0].Name, calls[1].Name)
	}
	if final.StopReason != msg.StopToolUse {
		t.Fatalf("StopReason = %q, want toolUse", final.StopReason)
	}
}

func TestFixtureOpenAIMalformedTruncated(t *testing.T) {
	body := loadFixture(t, "openai", "malformed_truncated", nil) // hand-authored; see file header
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := costedModel(provider.ApiOpenAICompletions, srv.URL+"/v1")
	client := &OpenAICompletionsClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{}, Auth{APIKey: "k"})
	all := collectEvents(events)
	_, err := wait()
	if err == nil {
		t.Fatal("expected an error for a stream that ends mid-event with no finish_reason")
	}
	last := all[len(all)-1]
	if last.Type != msg.EventError {
		t.Fatalf("last event = %q, want error", last.Type)
	}
	if last.Reason != msg.StopError {
		t.Fatalf("error reason = %q, want error", last.Reason)
	}
}

func TestFixtureOpenAIError429(t *testing.T) {
	testOpenAIErrorFixture(t, "error_429", 429, "rate_limit_error", true)
}

func TestFixtureOpenAIError529(t *testing.T) {
	testOpenAIErrorFixture(t, "error_529", 529, "overloaded_error", true)
}

func testOpenAIErrorFixture(t *testing.T, name string, status int, errType string, wantRetriable bool) {
	t.Helper()
	body := loadFixture(t, "openai", name, func() []byte {
		s, err := recordFauxErrorBody(t, "openai", status, errType, "synthetic error for conformance testing")
		if err != nil {
			t.Fatal(err)
		}
		return s
	})

	srv := replaySSE(t, status, "application/json", body)
	model := costedModel(provider.ApiOpenAICompletions, srv.URL+"/v1")
	client := &OpenAICompletionsClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{}, Auth{APIKey: "k"})
	_ = collectEvents(events)
	_, err := wait()
	if err == nil {
		t.Fatalf("expected an error for a %d response", status)
	}
	se, ok := asStatusError(err)
	if !ok {
		t.Fatalf("expected a *StatusError, got %T: %v", err, err)
	}
	if se.Status != status || se.Retriable != wantRetriable {
		t.Fatalf("status error = %+v, want status=%d retriable=%v", se, status, wantRetriable)
	}
	if !strings.Contains(se.Body, errType) {
		t.Fatalf("status error body = %q, want it to contain %q", se.Body, errType)
	}
}
