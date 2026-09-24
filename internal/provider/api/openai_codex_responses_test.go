package api

// Conformance tests for OpenAICodexResponsesClient, replayed against
// hand-authored SSE fixtures under testdata/openai-codex/*.sse (no
// faux-recording path exists for this shape, so every fixture here is
// hand-authored rather than recorded from a live server -- see the package
// doc comment on fixtures_test.go for the general fixture-provenance
// convention this suite otherwise follows).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// --- JWT test helpers ---

// buildTestJWT base64url-encodes header.payload.sig without any signing
// (this client never verifies the signature, only decodes the payload), so
// tests can construct a token with a known chatgpt_account_id claim.
func buildTestJWT(t *testing.T, payload map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	payloadPart := base64.RawURLEncoding.EncodeToString(payloadBytes)
	return header + "." + payloadPart + ".sig"
}

func testAuth(t *testing.T, accountID string) Auth {
	t.Helper()
	token := buildTestJWT(t, map[string]any{
		codexJWTClaimPath: map[string]any{"chatgpt_account_id": accountID},
	})
	return Auth{APIKey: token}
}

func codexCostedModel(baseURL string) provider.Model {
	m := costedModel(provider.ApiOpenAICodexResponses, baseURL)
	m.Input = []string{"text", "image"}
	return m
}

// ============================================================================
// JWT account-id extraction
// ============================================================================

func TestCodexExtractAccountID(t *testing.T) {
	token := buildTestJWT(t, map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct_123"},
	})
	id, err := extractCodexAccountID(token)
	if err != nil {
		t.Fatalf("extractCodexAccountID: %v", err)
	}
	if id != "acct_123" {
		t.Fatalf("account id = %q, want acct_123", id)
	}
}

func TestCodexExtractAccountIDMalformed(t *testing.T) {
	for _, tok := range []string{"not-a-jwt", "a.b", "a.b.c.d", ""} {
		if _, err := extractCodexAccountID(tok); err == nil {
			t.Fatalf("extractCodexAccountID(%q): expected an error", tok)
		}
	}
}

// ============================================================================
// Build-time auth failure (before any HTTP request)
// ============================================================================

func TestCodexStreamMalformedAuthErrorsBeforeRequest(t *testing.T) {
	model := codexCostedModel("http://127.0.0.1:0") // would refuse any real connection
	client := &OpenAICodexResponsesClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	events, wait := client.Stream(ctx, model, []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}},
	}, provider.StreamOptions{}, Auth{APIKey: "not-a-jwt"})
	all := collectEvents(events)
	_, err := wait()
	if err == nil {
		t.Fatal("expected an error for a malformed (non-JWT) auth token")
	}
	if len(all) != 1 || all[0].Type != msg.EventError {
		t.Fatalf("events = %+v, want exactly one error event (no HTTP request should have been attempted)", all)
	}
}

// ============================================================================
// Request body shape
// ============================================================================

func TestCodexBuildRequestInstructionsFromSystemMessage(t *testing.T) {
	model := codexCostedModel("")
	transcript := []msg.Message{
		msg.SystemMessage{Role: msg.RoleSystem, Content: msg.Blocks{msg.Text("You are a pirate.")}},
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}},
	}
	req := buildCodexRequest(model, transcript, provider.StreamOptions{})

	if req.Instructions != "You are a pirate." {
		t.Fatalf("instructions = %q, want %q", req.Instructions, "You are a pirate.")
	}
	for _, item := range req.Input {
		if role, _ := item["role"].(string); role == "system" || role == "developer" {
			t.Fatalf("input contains a %q-role item %+v, want the system message excluded from input (includeSystemPrompt:false)", role, item)
		}
	}
	if len(req.Input) != 1 || req.Input[0]["role"] != "user" {
		t.Fatalf("input = %+v, want exactly one user item", req.Input)
	}
}

func TestCodexBuildRequestDefaultInstructions(t *testing.T) {
	model := codexCostedModel("")
	req := buildCodexRequest(model, nil, provider.StreamOptions{})
	if req.Instructions != "You are a helpful assistant." {
		t.Fatalf("instructions = %q, want the default", req.Instructions)
	}
}

func TestCodexBuildRequestAlwaysPresentFields(t *testing.T) {
	model := codexCostedModel("")
	req := buildCodexRequest(model, nil, provider.StreamOptions{})

	if req.ToolChoice != "auto" {
		t.Fatalf("tool_choice = %q, want auto", req.ToolChoice)
	}
	if !req.ParallelToolCalls {
		t.Fatal("parallel_tool_calls = false, want true")
	}
	if req.Text.Verbosity != "low" {
		t.Fatalf("text.verbosity = %q, want low", req.Text.Verbosity)
	}
	if len(req.Include) != 1 || req.Include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include = %+v, want [reasoning.encrypted_content]", req.Include)
	}
	if req.Store {
		t.Fatal("store = true, want false")
	}
	if !req.Stream {
		t.Fatal("stream = false, want true")
	}

	// Marshal round trip: confirm the always-present fields survive JSON
	// encoding (e.g. include is not omitempty'd away).
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"tool_choice", "parallel_tool_calls", "text", "include", "store", "stream", "instructions", "input"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("marshaled body missing key %q: %s", key, body)
		}
	}
}

func TestCodexBuildRequestReasoningEffortMapping(t *testing.T) {
	model := codexCostedModel("")
	model.Reasoning = true
	mapped := "xhigh-mapped"
	model.ThinkingLevelMap = provider.ThinkingLevelMap{
		provider.ThinkingHigh: &mapped,
	}
	req := buildCodexRequest(model, nil, provider.StreamOptions{ThinkingLevel: provider.ThinkingHigh})
	if req.Reasoning == nil {
		t.Fatal("reasoning is nil, want it set")
	}
	if req.Reasoning.Effort != mapped {
		t.Fatalf("reasoning.effort = %q, want %q (mapped via model.ThinkingLevelMap)", req.Reasoning.Effort, mapped)
	}
	if req.Reasoning.Summary != "auto" {
		t.Fatalf("reasoning.summary = %q, want auto", req.Reasoning.Summary)
	}
}

func TestCodexBuildRequestReasoningUnmappedFallsBackToRawLevel(t *testing.T) {
	model := codexCostedModel("")
	model.Reasoning = true
	req := buildCodexRequest(model, nil, provider.StreamOptions{ThinkingLevel: provider.ThinkingMedium})
	if req.Reasoning == nil || req.Reasoning.Effort != "medium" {
		t.Fatalf("reasoning = %+v, want effort=medium (unmapped level falls back to the raw string)", req.Reasoning)
	}
}

func TestCodexToolsStrictModeDefaultTrue(t *testing.T) {
	tools := convertCodexTools([]provider.ToolDef{{Name: "search", Description: "search the web"}}, true)
	if len(tools) != 1 {
		t.Fatalf("tools = %+v", tools)
	}
	if strict, ok := tools[0]["strict"]; !ok || strict != true {
		t.Fatalf("tools[0][strict] = %v, want true", tools[0]["strict"])
	}
}

// ============================================================================
// SSE fixtures
// ============================================================================

func TestCodexFixtureTextOnly(t *testing.T) {
	body := loadFixture(t, "openai-codex", "text_only", nil)
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := codexCostedModel(srv.URL)
	client := &OpenAICodexResponsesClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	events, wait := client.Stream(ctx, model, []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}},
	}, provider.StreamOptions{}, testAuth(t, "acct_1"))
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
	if final.ResponseID != "resp_1" {
		t.Fatalf("ResponseID = %q, want resp_1", final.ResponseID)
	}
	if final.Usage.Input != 120 || final.Usage.Output != 40 {
		t.Fatalf("usage = %+v, want input=120 output=40", final.Usage)
	}
	assertCost(t, model, final.Usage)
	if final.API != string(provider.ApiOpenAICodexResponses) {
		t.Fatalf("API = %q, want %q", final.API, provider.ApiOpenAICodexResponses)
	}
}

func TestCodexFixtureReasoningText(t *testing.T) {
	body := loadFixture(t, "openai-codex", "reasoning_text", nil)
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := codexCostedModel(srv.URL)
	client := &OpenAICodexResponsesClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	events, wait := client.Stream(ctx, model, []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}},
	}, provider.StreamOptions{}, testAuth(t, "acct_1"))
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
	if thinking.ThinkingSignature == "" {
		t.Fatal("thinking signature is empty, want the raw reasoning item JSON")
	}
	var sigCheck map[string]any
	if err := json.Unmarshal([]byte(thinking.ThinkingSignature), &sigCheck); err != nil {
		t.Fatalf("thinking signature is not valid JSON: %v (%q)", err, thinking.ThinkingSignature)
	}
	if msg.TextOf(final.Content) != "Here is the answer." {
		t.Fatalf("text = %q", msg.TextOf(final.Content))
	}
	if final.Usage.Reasoning == nil || *final.Usage.Reasoning != 30 {
		t.Fatalf("usage.Reasoning = %v, want 30", final.Usage.Reasoning)
	}
	if final.StopReason != msg.StopStop {
		t.Fatalf("StopReason = %q, want stop", final.StopReason)
	}
}

func TestCodexFixtureFunctionCall(t *testing.T) {
	body := loadFixture(t, "openai-codex", "function_call", nil)
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := codexCostedModel(srv.URL)
	client := &OpenAICodexResponsesClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	events, wait := client.Stream(ctx, model, []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("check the weather")}},
	}, provider.StreamOptions{}, testAuth(t, "acct_1"))
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
	if calls[0].Arguments["query"] != "weather" {
		t.Fatalf("tool call args = %+v", calls[0].Arguments)
	}
	if final.StopReason != msg.StopToolUse {
		t.Fatalf("StopReason = %q, want toolUse (overridden from stop because the response contains a tool call)", final.StopReason)
	}
}

func TestCodexFixtureError429(t *testing.T) {
	testCodexErrorFixture(t, "error_429", 429, "rate_limit_error", true)
}

func TestCodexFixtureError529(t *testing.T) {
	testCodexErrorFixture(t, "error_529", 529, "overloaded_error", true)
}

func testCodexErrorFixture(t *testing.T, name string, status int, errType string, wantRetriable bool) {
	t.Helper()
	body := loadFixture(t, "openai-codex", name, nil)
	srv := replaySSE(t, status, "application/json", body)
	model := codexCostedModel(srv.URL)
	client := &OpenAICodexResponsesClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	events, wait := client.Stream(ctx, model, []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}},
	}, provider.StreamOptions{}, testAuth(t, "acct_1"))
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

func TestCodexFixtureTruncated(t *testing.T) {
	body := loadFixture(t, "openai-codex", "truncated", nil)
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := codexCostedModel(srv.URL)
	client := &OpenAICodexResponsesClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	events, wait := client.Stream(ctx, model, []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}},
	}, provider.StreamOptions{}, testAuth(t, "acct_1"))
	all := collectEvents(events)
	_, err := wait()
	if err == nil {
		t.Fatal("expected an error for a stream that ends mid-event with no terminal response event")
	}
	last := all[len(all)-1]
	if last.Type != msg.EventError {
		t.Fatalf("last event = %q, want error", last.Type)
	}
	if last.Reason != msg.StopError {
		t.Fatalf("error reason = %q, want error", last.Reason)
	}
}
