package api

// Conformance tests for OpenAIResponsesClient, replayed against
// hand-authored SSE fixtures under testdata/openai-responses/*.sse. There is
// no faux-server recording path for this API shape (internal/testkit/faux
// only knows anthropic-messages and openai-completions wire shapes), so
// every fixture here is hand-authored SSE text rather than recorded from a
// live server -- see the individual test functions for what each fixture
// exercises. Reuses costedModel, replaySSE, collectEvents,
// assertPartialIdentity, assertTextMonotonic, assertCost and asStatusError
// from fixtures_test.go / conformance_test.go (same package, not edited by
// this change).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

func loadResponsesFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "openai-responses", name+".sse"))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}

func responsesModel(baseURL string) provider.Model {
	m := costedModel(provider.ApiOpenAIResponses, baseURL)
	return m
}

// ============================================================================
// Fixture 1: text_only
// ============================================================================

func TestFixtureOpenAIResponsesTextOnly(t *testing.T) {
	body := loadResponsesFixture(t, "text_only")
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := responsesModel(srv.URL)
	client := &OpenAIResponsesClient{}
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
	if final.RawStopReason != "completed" {
		t.Fatalf("RawStopReason = %q, want completed", final.RawStopReason)
	}
	if final.ResponseID != "resp_text_only" {
		t.Fatalf("ResponseID = %q", final.ResponseID)
	}
	if final.Usage.Input != 120 || final.Usage.Output != 40 {
		t.Fatalf("usage = %+v, want input=120 output=40", final.Usage)
	}
	assertCost(t, model, final.Usage)
}

// ============================================================================
// Fixture 2: reasoning_text
// ============================================================================

func TestFixtureOpenAIResponsesReasoningText(t *testing.T) {
	body := loadResponsesFixture(t, "reasoning_text")
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := responsesModel(srv.URL)
	client := &OpenAIResponsesClient{}
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
	if !ok || thinking.Thinking != "Thinking about it." {
		t.Fatalf("content[0] = %+v, want thinking %q", final.Content[0], "Thinking about it.")
	}
	if !json.Valid([]byte(thinking.ThinkingSignature)) {
		t.Fatalf("ThinkingSignature is not valid JSON: %q", thinking.ThinkingSignature)
	}
	if msg.TextOf(final.Content) != "Here's the answer." {
		t.Fatalf("text = %q", msg.TextOf(final.Content))
	}
	if final.StopReason != msg.StopStop {
		t.Fatalf("StopReason = %q, want stop", final.StopReason)
	}
}

// ============================================================================
// Fixture 3: function_call
// ============================================================================

func TestFixtureOpenAIResponsesFunctionCall(t *testing.T) {
	body := loadResponsesFixture(t, "function_call")
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := responsesModel(srv.URL)
	client := &OpenAIResponsesClient{}
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
	if calls[0].ID != "call_1|fc_1" {
		t.Fatalf("tool call id = %q, want %q (callId|itemId)", calls[0].ID, "call_1|fc_1")
	}
	if calls[0].Arguments["query"] != "weather" {
		t.Fatalf("tool call args = %+v", calls[0].Arguments)
	}
	// openai-responses-shared.js finalizeResponse: a toolCall content block
	// present overrides stopReason "stop" -> "toolUse" even though the
	// response's status was "completed".
	if final.StopReason != msg.StopToolUse {
		t.Fatalf("StopReason = %q, want toolUse (overridden from completed/stop)", final.StopReason)
	}
	if final.RawStopReason != "completed" {
		t.Fatalf("RawStopReason = %q, want completed", final.RawStopReason)
	}
}

// ============================================================================
// Fixture 4: usage_cache
// ============================================================================

func TestFixtureOpenAIResponsesUsageCache(t *testing.T) {
	body := loadResponsesFixture(t, "usage_cache")
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := responsesModel(srv.URL)
	client := &OpenAIResponsesClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{}, Auth{APIKey: "k"})
	_ = collectEvents(events)
	final, err := wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}

	// input_tokens(220) - cached_tokens(100) - cache_write_tokens(0) = 120.
	if final.Usage.Input != 120 {
		t.Fatalf("Usage.Input = %d, want 120", final.Usage.Input)
	}
	if final.Usage.CacheRead != 100 {
		t.Fatalf("Usage.CacheRead = %d, want 100", final.Usage.CacheRead)
	}
	if final.Usage.Reasoning == nil || *final.Usage.Reasoning != 25 {
		t.Fatalf("Usage.Reasoning = %v, want 25", final.Usage.Reasoning)
	}
	assertCost(t, model, final.Usage)
}

// ============================================================================
// Fixtures 5/6: error_429, error_529
// ============================================================================

func TestFixtureOpenAIResponsesError429(t *testing.T) {
	testOpenAIResponsesErrorFixture(t, "error_429", 429, "rate_limit_error", true)
}

func TestFixtureOpenAIResponsesError529(t *testing.T) {
	testOpenAIResponsesErrorFixture(t, "error_529", 529, "overloaded_error", true)
}

func testOpenAIResponsesErrorFixture(t *testing.T, name string, status int, errType string, wantRetriable bool) {
	t.Helper()
	body := loadResponsesFixture(t, name)
	srv := replaySSE(t, status, "application/json", body)
	model := responsesModel(srv.URL)
	client := &OpenAIResponsesClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{}, Auth{APIKey: "k"})
	_ = collectEvents(events)
	_, err := wait()
	if err == nil {
		t.Fatalf("expected an error for a %d response", status)
	}
	var se *StatusError
	if !errors.As(err, &se) {
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
// Fixture 7 (truncated): stream ends without a terminal event
// ============================================================================

func TestFixtureOpenAIResponsesTruncated(t *testing.T) {
	body := loadResponsesFixture(t, "truncated")
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := responsesModel(srv.URL)
	client := &OpenAIResponsesClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{}, Auth{APIKey: "k"})
	all := collectEvents(events)
	_, err := wait()
	if err == nil {
		t.Fatal("expected an error for a stream that ends mid-event with no terminal response event")
	}
	if !strings.Contains(err.Error(), "terminal response event") {
		t.Fatalf("error = %q, want it to mention a missing terminal response event", err.Error())
	}
	last := all[len(all)-1]
	if last.Type != msg.EventError {
		t.Fatalf("last event = %q, want error", last.Type)
	}
	if last.Reason != msg.StopError {
		t.Fatalf("error reason = %q, want error", last.Reason)
	}
}

// ============================================================================
// Request-shape tests: buildOpenAIResponsesRequest, no HTTP.
// ============================================================================

func responsesReasoningModel() provider.Model {
	m := costedModel(provider.ApiOpenAIResponses, "https://example.test")
	m.Reasoning = true
	m.ThinkingLevelMap = provider.ThinkingLevelMap{
		provider.ThinkingHigh: strPtr("high"),
	}
	return m
}

// strPtr is defined once for the package in openai_completions_thinking_test.go.

func TestBuildOpenAIResponsesRequest_ReasoningHigh(t *testing.T) {
	model := responsesReasoningModel()
	transcript := []msg.Message{
		msg.SystemMessage{Role: msg.RoleSystem, Content: msg.Blocks{msg.Text("You are a helpful assistant.")}},
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{
			msg.Text("what's in this image?"),
			msg.Image("image/png", "aGVsbG8="),
		}},
		msg.AssistantMessage{
			Role: msg.RoleAssistant,
			Content: msg.Blocks{
				msg.NewToolCall("call_1|fc_1", "search", map[string]any{"query": "cats"}),
			},
		},
		msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: "call_1|fc_1", ToolName: "search", Content: msg.Blocks{msg.Text("no results")}},
	}

	req := buildOpenAIResponsesRequest(model, transcript, provider.StreamOptions{ThinkingLevel: provider.ThinkingHigh})

	// openai-responses.js buildParams: reasoning.effort falls back to
	// model.thinkingLevelMap[level] ?? level; summary is always "auto";
	// include carries "reasoning.encrypted_content".
	if req.Reasoning == nil || req.Reasoning.Effort != "high" || req.Reasoning.Summary != "auto" {
		t.Fatalf("Reasoning = %+v, want {effort:high summary:auto}", req.Reasoning)
	}
	if len(req.Include) != 1 || req.Include[0] != "reasoning.encrypted_content" {
		t.Fatalf("Include = %v, want [reasoning.encrypted_content]", req.Include)
	}

	if len(req.Input) != 4 {
		t.Fatalf("Input items = %d, want 4 (instructions, user, function_call, function_call_output)", len(req.Input))
	}

	// openai-responses-shared.js convertResponsesMessages: leading system
	// message -> {role: instructionRole, content}; instructionRole is
	// "developer" because model.reasoning && compat.supportsDeveloperRole
	// (default true).
	var instr map[string]any
	if err := json.Unmarshal(req.Input[0], &instr); err != nil {
		t.Fatal(err)
	}
	if instr["role"] != "developer" || instr["content"] != "You are a helpful assistant." {
		t.Fatalf("instructions item = %+v", instr)
	}

	// User message: input_text + input_image with a data URL.
	var userItem map[string]any
	if err := json.Unmarshal(req.Input[1], &userItem); err != nil {
		t.Fatal(err)
	}
	if userItem["role"] != "user" {
		t.Fatalf("user item = %+v", userItem)
	}
	content := userItem["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("user content parts = %d, want 2", len(content))
	}
	textPart := content[0].(map[string]any)
	if textPart["type"] != "input_text" || textPart["text"] != "what's in this image?" {
		t.Fatalf("text part = %+v", textPart)
	}
	imagePart := content[1].(map[string]any)
	if imagePart["type"] != "input_image" || imagePart["detail"] != "auto" || imagePart["image_url"] != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("image part = %+v", imagePart)
	}

	// Assistant tool call: {type:"function_call", id:<itemId>, call_id:<callId>, ...}.
	var fc map[string]any
	if err := json.Unmarshal(req.Input[2], &fc); err != nil {
		t.Fatal(err)
	}
	if fc["type"] != "function_call" || fc["call_id"] != "call_1" || fc["id"] != "fc_1" || fc["name"] != "search" {
		t.Fatalf("function_call item = %+v", fc)
	}
	var fcArgs map[string]any
	if err := json.Unmarshal([]byte(fc["arguments"].(string)), &fcArgs); err != nil {
		t.Fatal(err)
	}
	if fcArgs["query"] != "cats" {
		t.Fatalf("function_call arguments = %+v", fcArgs)
	}

	// Tool result: {type:"function_call_output", call_id:<callId>, output:<text>}.
	var fco map[string]any
	if err := json.Unmarshal(req.Input[3], &fco); err != nil {
		t.Fatal(err)
	}
	if fco["type"] != "function_call_output" || fco["call_id"] != "call_1" || fco["output"] != "no results" {
		t.Fatalf("function_call_output item = %+v", fco)
	}
}

func TestBuildOpenAIResponsesRequest_ThinkingOff(t *testing.T) {
	model := responsesReasoningModel()
	transcript := []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}},
	}

	req := buildOpenAIResponsesRequest(model, transcript, provider.StreamOptions{ThinkingLevel: provider.ThinkingOff})

	// openai-responses.js buildParams: no reasoningEffort/reasoningSummary
	// and model.provider !== "github-copilot" && model.thinkingLevelMap?.off
	// !== null -> {effort: model.thinkingLevelMap?.off ?? "none"}. This
	// model's ThinkingLevelMap has no "off" key at all, so the fallback
	// "none" applies.
	if req.Reasoning == nil || req.Reasoning.Effort != "none" {
		t.Fatalf("Reasoning = %+v, want {effort:none}", req.Reasoning)
	}
	if req.Reasoning.Summary != "" {
		t.Fatalf("Reasoning.Summary = %q, want empty (summary is only sent on the reasoningEffort/reasoningSummary branch)", req.Reasoning.Summary)
	}
	if req.Include != nil {
		t.Fatalf("Include = %v, want nil (include is only sent on the reasoningEffort/reasoningSummary branch)", req.Include)
	}
}

func TestBuildOpenAIResponsesRequest_ThinkingOffExplicitNullOmitsReasoning(t *testing.T) {
	model := responsesReasoningModel()
	model.ThinkingLevelMap[provider.ThinkingOff] = nil // explicit JS null: omit reasoning entirely
	transcript := []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}},
	}

	req := buildOpenAIResponsesRequest(model, transcript, provider.StreamOptions{ThinkingLevel: provider.ThinkingOff})

	// openai-responses.js buildParams: `model.thinkingLevelMap?.off !== null`
	// guards the whole else-if; a present key mapped to null means send no
	// reasoning field at all.
	if req.Reasoning != nil {
		t.Fatalf("Reasoning = %+v, want nil (thinkingLevelMap.off is explicit null)", req.Reasoning)
	}
}

func TestBuildOpenAIResponsesRequest_MaxOutputTokensClampedToMinimum(t *testing.T) {
	model := costedModel(provider.ApiOpenAIResponses, "https://example.test")
	req := buildOpenAIResponsesRequest(model, nil, provider.StreamOptions{MaxTokens: 4})
	// openai-responses.js: Math.max(options.maxTokens, OPENAI_RESPONSES_MIN_OUTPUT_TOKENS=16).
	if req.MaxOutputTokens != 16 {
		t.Fatalf("MaxOutputTokens = %d, want 16 (clamped up from 4)", req.MaxOutputTokens)
	}
}
