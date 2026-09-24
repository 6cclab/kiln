package api

// Conformance and unit tests for AzureOpenAIResponsesClient, replayed
// against hand-authored SSE fixtures under testdata/azure/*.sse (there is no
// faux-server recording path for this API shape this phase; see
// fixtures_test.go's provenance comment for the analogous
// anthropic/openai-completions convention this mirrors).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

func azureCostedModel(baseURL string, reasoning bool) provider.Model {
	m := costedModel(provider.ApiAzureOpenAIResponses, baseURL)
	m.Reasoning = reasoning
	return m
}

func loadAzureFixture(t *testing.T, name string) []byte {
	t.Helper()
	return loadFixture(t, "azure", name, nil)
}

// ============================================================================
// Conformance
// ============================================================================

func TestFixtureAzureTextOnly(t *testing.T) {
	body := loadAzureFixture(t, "text_only")
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := azureCostedModel(srv.URL, false)
	client := &AzureOpenAIResponsesClient{}
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
	if final.ResponseID != "resp_1" {
		t.Fatalf("ResponseID = %q, want resp_1", final.ResponseID)
	}
	if final.Usage.Input != 120 || final.Usage.Output != 40 {
		t.Fatalf("usage = %+v, want input=120 output=40", final.Usage)
	}
	if final.API != string(provider.ApiAzureOpenAIResponses) {
		t.Fatalf("API = %q, want %q", final.API, provider.ApiAzureOpenAIResponses)
	}
	assertCost(t, model, final.Usage)
}

// TestFixtureAzureReasoningTextBackfill is the headline Azure-specific test:
// output_item.done for the reasoning item carries no encrypted_content, but
// response.completed's response.output array includes that same reasoning
// item WITH encrypted_content. The final ThinkingSignature must end up with
// encrypted_content merged in by the backfill step (mirroring pi's
// backfillReasoningSignatures in openai-responses-shared.js).
func TestFixtureAzureReasoningTextBackfill(t *testing.T) {
	body := loadAzureFixture(t, "reasoning_text")
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := azureCostedModel(srv.URL, true)
	client := &AzureOpenAIResponsesClient{}
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
	if !ok {
		t.Fatalf("content[0] = %+v, want ThinkingContent", final.Content[0])
	}
	if thinking.Thinking != "Thinking it over." {
		t.Fatalf("thinking text = %q, want %q", thinking.Thinking, "Thinking it over.")
	}
	if thinking.ThinkingSignature == "" {
		t.Fatal("ThinkingSignature is empty")
	}
	var sig map[string]any
	if err := json.Unmarshal([]byte(thinking.ThinkingSignature), &sig); err != nil {
		t.Fatalf("ThinkingSignature is not valid JSON: %v", err)
	}
	if got := sig["encrypted_content"]; got != "enc_abc123" {
		t.Fatalf("ThinkingSignature.encrypted_content = %v, want %q (backfill from response.completed.response.output did not run)", got, "enc_abc123")
	}

	if msg.TextOf(final.Content) != "Here is the answer." {
		t.Fatalf("text = %q", msg.TextOf(final.Content))
	}
	if final.StopReason != msg.StopStop {
		t.Fatalf("StopReason = %q, want stop", final.StopReason)
	}
	if final.Usage.Reasoning == nil || *final.Usage.Reasoning != 30 {
		t.Fatalf("Usage.Reasoning = %v, want 30", final.Usage.Reasoning)
	}
	assertCost(t, model, final.Usage)
}

func TestFixtureAzureFunctionCall(t *testing.T) {
	body := loadAzureFixture(t, "function_call")
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := azureCostedModel(srv.URL, false)
	client := &AzureOpenAIResponsesClient{}
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
	if calls[0].Arguments["query"] != "weather" {
		t.Fatalf("tool call args = %+v", calls[0].Arguments)
	}
	if final.StopReason != msg.StopToolUse {
		t.Fatalf("StopReason = %q, want toolUse", final.StopReason)
	}
	if final.RawStopReason != "completed" {
		t.Fatalf("RawStopReason = %q, want completed", final.RawStopReason)
	}
}

func TestFixtureAzureTruncated(t *testing.T) {
	body := loadAzureFixture(t, "truncated")
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := azureCostedModel(srv.URL, false)
	client := &AzureOpenAIResponsesClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{}, Auth{APIKey: "k"})
	all := collectEvents(events)
	_, err := wait()
	if err == nil {
		t.Fatal("expected an error for a stream that ends with no terminal response event")
	}
	last := all[len(all)-1]
	if last.Type != msg.EventError {
		t.Fatalf("last event = %q, want error", last.Type)
	}
	if last.Reason != msg.StopError {
		t.Fatalf("error reason = %q, want error", last.Reason)
	}
}

func TestFixtureAzureError429(t *testing.T) {
	testAzureErrorFixture(t, "error_429", 429, "rate_limit_error", true)
}

func TestFixtureAzureError529(t *testing.T) {
	testAzureErrorFixture(t, "error_529", 529, "overloaded_error", true)
}

func testAzureErrorFixture(t *testing.T, name string, status int, errType string, wantRetriable bool) {
	t.Helper()
	body := loadAzureFixture(t, name)
	srv := replaySSE(t, status, "application/json", body)
	model := azureCostedModel(srv.URL, false)
	client := &AzureOpenAIResponsesClient{}
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
// URL normalization
// ============================================================================

func TestNormalizeAzureBaseURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://my-resource.openai.azure.com", "https://my-resource.openai.azure.com/openai/v1"},
		{"https://my-resource.openai.azure.com/", "https://my-resource.openai.azure.com/openai/v1"},
		{"https://my-resource.openai.azure.com/openai", "https://my-resource.openai.azure.com/openai/v1"},
		{"https://my-resource.cognitiveservices.azure.com/openai/v1/responses", "https://my-resource.cognitiveservices.azure.com/openai/v1"},
		{"https://my-gateway.example.com/openai/v1", "https://my-gateway.example.com/openai/v1"},
	}
	for _, tc := range cases {
		got, err := normalizeAzureBaseURL(tc.in)
		if err != nil {
			t.Fatalf("normalizeAzureBaseURL(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("normalizeAzureBaseURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ============================================================================
// Request shape
// ============================================================================

func TestAzureRequestURLAndAuth(t *testing.T) {
	var capturedPath, capturedQuery, capturedAPIKey, capturedAuthHeader string
	var capturedBody []byte
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedQuery = r.URL.RawQuery
		capturedAPIKey = r.Header.Get("api-key")
		capturedAuthHeader = r.Header.Get("Authorization")
		b, _ := jsonReadAll(r)
		capturedBody = b
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_x\",\"status\":\"in_progress\"}}\n\n" +
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_x\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer srv2.Close()

	model := azureCostedModel(srv2.URL, false)
	model.ID = "my-deployment"
	client := &AzureOpenAIResponsesClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{}, Auth{APIKey: "secret-key"})
	collectEvents(events)
	if _, err := wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}

	if capturedPath != "/responses" {
		t.Fatalf("path = %q, want /responses", capturedPath)
	}
	if capturedQuery != "api-version=v1" {
		t.Fatalf("query = %q, want api-version=v1", capturedQuery)
	}
	if capturedAPIKey != "secret-key" {
		t.Fatalf("api-key header = %q, want secret-key", capturedAPIKey)
	}
	if capturedAuthHeader != "" {
		t.Fatalf("Authorization header = %q, want empty (Azure uses api-key, not Bearer)", capturedAuthHeader)
	}

	var req azureRequest
	if err := json.Unmarshal(capturedBody, &req); err != nil {
		t.Fatalf("request body is not valid JSON: %v (body=%s)", err, capturedBody)
	}
	if req.Model != "my-deployment" {
		t.Fatalf("body.model = %q, want %q (deployment name fallback = model.ID)", req.Model, "my-deployment")
	}
	if req.Store {
		t.Fatal("body.store = true, want false (Azure always sends store:false)")
	}
}

func jsonReadAll(r *http.Request) ([]byte, error) {
	buf := make([]byte, 0, 4096)
	chunk := make([]byte, 4096)
	for {
		n, err := r.Body.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
		}
		if err != nil {
			break
		}
	}
	return buf, nil
}

// ============================================================================
// Reasoning request shape
// ============================================================================

func TestAzureRequestReasoningHigh(t *testing.T) {
	model := azureCostedModel("https://example.com", true)
	opts := provider.StreamOptions{ThinkingLevel: provider.ThinkingHigh}
	req := buildAzureRequest(model, nil, opts)
	if req.Reasoning == nil {
		t.Fatal("Reasoning is nil, want set for ThinkingLevel=high on a reasoning model")
	}
	if req.Reasoning.Effort != "high" {
		t.Fatalf("Reasoning.Effort = %q, want high", req.Reasoning.Effort)
	}
	if req.Reasoning.Summary != "auto" {
		t.Fatalf("Reasoning.Summary = %q, want auto", req.Reasoning.Summary)
	}
	if len(req.Include) != 1 || req.Include[0] != "reasoning.encrypted_content" {
		t.Fatalf("Include = %v, want [reasoning.encrypted_content]", req.Include)
	}
}

func TestAzureRequestReasoningOff(t *testing.T) {
	model := azureCostedModel("https://example.com", true)
	opts := provider.StreamOptions{ThinkingLevel: provider.ThinkingOff}
	req := buildAzureRequest(model, nil, opts)
	if req.Reasoning == nil {
		t.Fatal("Reasoning is nil, want {effort: none} for ThinkingLevel=off on a reasoning model")
	}
	if req.Reasoning.Effort != "none" {
		t.Fatalf("Reasoning.Effort = %q, want none", req.Reasoning.Effort)
	}
	if req.Reasoning.Summary != "" {
		t.Fatalf("Reasoning.Summary = %q, want empty (no summary sent for the off/default reasoning shape)", req.Reasoning.Summary)
	}
	if req.Include != nil {
		t.Fatalf("Include = %v, want nil", req.Include)
	}
}
