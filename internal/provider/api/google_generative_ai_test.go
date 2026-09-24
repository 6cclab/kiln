package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// Fixtures under testdata/google/*.sse are hand-authored (there is no faux
// server for this shape; see the phase's instructions) against the
// GenerateContentResponse JSON shape @google/genai's SDK decodes from
// `alt=sse` chunks -- the same shape google-generative-ai.js consumes as
// `chunk.candidates[0].content.parts`/`chunk.usageMetadata` (see
// google_generative_ai.go's run()).

func googleModel(baseURL string) provider.Model {
	return provider.Model{
		ID:            "gemini-2.5-pro",
		Name:          "gemini-2.5-pro",
		Api:           provider.ApiGoogleGenerativeAI,
		Provider:      "google",
		BaseURL:       baseURL,
		Reasoning:     true,
		ContextWindow: 1_000_000,
		MaxTokens:     8192,
		Cost: provider.ModelCost{
			ModelCostRates: provider.ModelCostRates{Input: 1.25, Output: 10, CacheRead: 0.31},
		},
	}
}

func loadGoogleFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/google/" + name + ".sse")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return body
}

func TestGoogleGenerativeAITextOnly(t *testing.T) {
	body := loadGoogleFixture(t, "text_only")
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := googleModel(srv.URL)
	client := &GoogleGenerativeAIClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	transcript := []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}
	events, wait := client.Stream(ctx, model, transcript, provider.StreamOptions{}, Auth{APIKey: "k"})
	all := collectEvents(events)
	final, err := wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	assertPartialIdentity(t, all, final)
	assertTextMonotonic(t, all)

	if msg.TextOf(final.Content) != "Hello world." {
		t.Fatalf("text = %q", msg.TextOf(final.Content))
	}
	if final.StopReason != msg.StopStop {
		t.Fatalf("StopReason = %q, want stop", final.StopReason)
	}
	if final.RawStopReason != "STOP" {
		t.Fatalf("RawStopReason = %q, want STOP", final.RawStopReason)
	}
	if final.ResponseID != "resp-1" {
		t.Fatalf("ResponseID = %q", final.ResponseID)
	}
	if final.Usage.Input != 10 || final.Usage.Output != 5 {
		t.Fatalf("usage = %+v", final.Usage)
	}
	assertCost(t, model, final.Usage)
}

func TestGoogleGenerativeAIThinkingAndToolCall(t *testing.T) {
	body := loadGoogleFixture(t, "thinking_tool")
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := googleModel(srv.URL)
	client := &GoogleGenerativeAIClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	transcript := []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("weather?")}}}
	events, wait := client.Stream(ctx, model, transcript, provider.StreamOptions{ThinkingLevel: provider.ThinkingHigh}, Auth{APIKey: "k"})
	all := collectEvents(events)
	final, err := wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	assertPartialIdentity(t, all, final)

	if len(final.Content) != 2 {
		t.Fatalf("content blocks = %d, want 2 (thinking, toolCall)", len(final.Content))
	}
	thinking, ok := final.Content[0].(msg.ThinkingContent)
	if !ok || thinking.Thinking != "Thinking about the question." {
		t.Fatalf("content[0] = %+v", final.Content[0])
	}
	if thinking.ThinkingSignature != "c2lnbmF0dXJl" {
		t.Fatalf("thinking signature = %q, want the round-tripped thoughtSignature", thinking.ThinkingSignature)
	}
	calls := msg.ToolCallsOf(final.Content)
	if len(calls) != 1 || calls[0].Name != "search" || calls[0].Arguments["query"] != "weather in san francisco" {
		t.Fatalf("tool calls = %+v", calls)
	}
	// google-generative-ai.js:164-166: "output.stopReason = mapStopReason(...); if (has toolCall && stopReason === "stop") stopReason = "toolUse""
	if final.StopReason != msg.StopToolUse {
		t.Fatalf("StopReason = %q, want toolUse (STOP + a toolCall block remaps to toolUse)", final.StopReason)
	}
	if final.Usage.CacheRead != 4 {
		t.Fatalf("cacheRead = %d, want 4 (cachedContentTokenCount)", final.Usage.CacheRead)
	}
	if final.Usage.Reasoning == nil || *final.Usage.Reasoning != 3 {
		t.Fatalf("reasoning = %v, want 3 (thoughtsTokenCount)", final.Usage.Reasoning)
	}
	// input = promptTokenCount - cachedContentTokenCount = 20 - 4 = 16 (google-shared.js usage mapping)
	if final.Usage.Input != 16 {
		t.Fatalf("input = %d, want 16 (20 prompt - 4 cached)", final.Usage.Input)
	}
	// output = candidatesTokenCount + thoughtsTokenCount = 8 + 3 = 11
	if final.Usage.Output != 11 {
		t.Fatalf("output = %d, want 11 (8 candidates + 3 thoughts)", final.Usage.Output)
	}
}

func TestGoogleGenerativeAIMalformedTruncated(t *testing.T) {
	body := loadGoogleFixture(t, "malformed_truncated")
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := googleModel(srv.URL)
	client := &GoogleGenerativeAIClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{}, Auth{APIKey: "k"})
	all := collectEvents(events)
	_, err := wait()
	if err == nil {
		t.Fatal("expected an error for a stream that never sends a finishReason")
	}
	last := all[len(all)-1]
	if last.Type != msg.EventError {
		t.Fatalf("last event = %q, want error", last.Type)
	}
}

func TestGoogleGenerativeAIError(t *testing.T) {
	errBody := `{"error":{"code":429,"message":"rate limited","status":"RESOURCE_EXHAUSTED"}}`
	srv := replaySSE(t, http.StatusTooManyRequests, "application/json", []byte(errBody))
	model := googleModel(srv.URL)
	client := &GoogleGenerativeAIClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{}, Auth{APIKey: "k"})
	_ = collectEvents(events)
	_, err := wait()
	if err == nil {
		t.Fatal("expected an error for a 429 response")
	}
	se, ok := asStatusError(err)
	if !ok {
		t.Fatalf("expected a *StatusError, got %T: %v", err, err)
	}
	if se.Status != 429 || !se.Retriable {
		t.Fatalf("status error = %+v, want status=429 retriable=true", se)
	}
}

// TestBuildGoogleRequestShape asserts the JSON body this client sends
// matches what google-generative-ai.js's buildParams would hand the SDK
// (which serializes to this exact GenerateContentRequest field set):
//
//	config.systemInstruction = sanitizeSurrogates(systemInstruction)       (google-generative-ai.js:302)
//	config.tools = convertTools(currentTools, false, supportsStrictMode)   (line 304, parametersJsonSchema path)
//	config.thinkingConfig = {includeThoughts:true, thinkingBudget: N}      (lines 311-318, budget path for non-Gemini-3 ids)
func TestBuildGoogleRequestShape(t *testing.T) {
	model := googleModel("https://example.test")
	model.ID = "gemini-2.5-pro" // budget-table path, not thinkingLevel
	transcript := []msg.Message{
		msg.SystemMessage{Role: msg.RoleSystem, Content: msg.Blocks{msg.Text("You are helpful.")}},
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}},
	}
	opts := provider.StreamOptions{
		ThinkingLevel: provider.ThinkingHigh,
		Tools:         []provider.ToolDef{{Name: "search", Description: "search the web", Parameters: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)}},
	}
	req := buildGoogleRequest(model, transcript, opts)

	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded["systemInstruction"] == nil {
		t.Fatal("systemInstruction missing")
	}
	tools, ok := decoded["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v, want one googleTool wrapper", decoded["tools"])
	}
	gc, ok := decoded["generationConfig"].(map[string]any)
	if !ok {
		t.Fatal("generationConfig missing")
	}
	tc, ok := gc["thinkingConfig"].(map[string]any)
	if !ok {
		t.Fatal("generationConfig.thinkingConfig missing")
	}
	if tc["includeThoughts"] != true {
		t.Fatalf("thinkingConfig.includeThoughts = %v, want true", tc["includeThoughts"])
	}
	if tc["thinkingBudget"] != float64(32768) {
		t.Fatalf("thinkingConfig.thinkingBudget = %v, want 32768 (2.5-pro high budget)", tc["thinkingBudget"])
	}
	if _, hasLevel := tc["thinkingLevel"]; hasLevel {
		t.Fatal("thinkingLevel should not be set for a budget-table model")
	}
}

// TestUsesGoogleThinkingLevel exercises the regex ported from
// google-shared.js: "/gemini-3(?:\.\d+)?-(?:pro|flash)/.test(id) ||
// id === 'gemini-flash-latest' || ... || /gemma-?4/.test(id)".
func TestUsesGoogleThinkingLevel(t *testing.T) {
	cases := map[string]bool{
		"gemini-3-pro-preview":     true,
		"gemini-3.1-flash-preview": true,
		"gemini-flash-latest":      true,
		"gemma-4-27b":              true,
		"gemma4-9b":                true,
		"gemini-2.5-pro":           false,
		"gemini-2.5-flash":         false,
	}
	for id, want := range cases {
		if got := usesGoogleThinkingLevel(id); got != want {
			t.Errorf("usesGoogleThinkingLevel(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestGoogleHeadersUsesAPIKeyHeader(t *testing.T) {
	h := googleHeaders(provider.Model{}, Auth{APIKey: "secret"})
	if got := h.Get("x-goog-api-key"); got != "secret" {
		t.Fatalf("x-goog-api-key = %q, want secret", got)
	}
	if h.Get("Authorization") != "" {
		t.Fatal("Google Generative AI does not use an Authorization header")
	}
}

func TestGoogleEndpointURL(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	model := googleModel(srv.URL)
	model.ID = "gemini-2.5-flash"
	client := &GoogleGenerativeAIClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{}, Auth{APIKey: "k"})
	_ = collectEvents(events)
	_, _ = wait()

	want := "/models/gemini-2.5-flash:streamGenerateContent?alt=sse"
	if !strings.HasSuffix(gotURL, want) {
		t.Fatalf("request URL = %q, want suffix %q", gotURL, want)
	}
}
