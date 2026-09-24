package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// Fixtures under testdata/pi/*.sse are hand-authored against pi-messages.js's
// own wire shape: "the response is an SSE stream of serialized
// assistant-message events plus a terminal `done`/`error` event"
// (pi-messages.js:1-10), where each event is exactly the JSON shape
// createEventConverter's `event` parameter documents -- i.e. msg.StreamEvent
// serialized with pi's field names (contentIndex, toolName, contentSignature,
// toolCall, reason, usage, responseId).

func piModel(baseURL string) provider.Model {
	return provider.Model{
		ID:            "radius-default",
		Name:          "radius-default",
		Api:           provider.ApiPiMessages,
		Provider:      "radius",
		BaseURL:       baseURL,
		ContextWindow: 128000,
		MaxTokens:     4096,
	}
}

func loadPiFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/pi/" + name + ".sse")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return body
}

func TestPiMessagesTextOnly(t *testing.T) {
	body := loadPiFixture(t, "text_only")
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := piModel(srv.URL)
	client := &PiMessagesClient{}
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

	if msg.TextOf(final.Content) != "Hello world." {
		t.Fatalf("text = %q", msg.TextOf(final.Content))
	}
	if final.StopReason != msg.StopStop {
		t.Fatalf("StopReason = %q, want stop", final.StopReason)
	}
	if final.ResponseID != "pi-resp-1" {
		t.Fatalf("ResponseID = %q", final.ResponseID)
	}
	if final.Usage.Input != 10 || final.Usage.Output != 4 || final.Usage.TotalTokens != 14 {
		t.Fatalf("usage = %+v", final.Usage)
	}
	if final.Usage.Cost.Total != 0.00009 {
		t.Fatalf("cost.total = %v, want 0.00009 (the backend's own usage.cost is trusted verbatim, not recomputed)", final.Usage.Cost.Total)
	}
}

func TestPiMessagesThinkingAndToolCall(t *testing.T) {
	body := loadPiFixture(t, "thinking_tool")
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := piModel(srv.URL)
	client := &PiMessagesClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("weather?")}}}, provider.StreamOptions{}, Auth{APIKey: "k"})
	all := collectEvents(events)
	final, err := wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	assertPartialIdentity(t, all, final)

	if len(final.Content) != 2 {
		t.Fatalf("content blocks = %d, want 2", len(final.Content))
	}
	thinking, ok := final.Content[0].(msg.ThinkingContent)
	if !ok || thinking.Thinking != "Let me check that." || thinking.ThinkingSignature != "sig-abc" {
		t.Fatalf("content[0] = %+v", final.Content[0])
	}
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
	if len(calls) != 1 || calls[0].Name != "search" || calls[0].ID != "tc1" || calls[0].Arguments["query"] != "weather" {
		t.Fatalf("tool calls = %+v", calls)
	}
	if final.StopReason != msg.StopToolUse {
		t.Fatalf("StopReason = %q, want toolUse", final.StopReason)
	}
}

func TestPiMessagesMalformedTruncated(t *testing.T) {
	body := loadPiFixture(t, "malformed_truncated")
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := piModel(srv.URL)
	client := &PiMessagesClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{}, Auth{APIKey: "k"})
	all := collectEvents(events)
	_, err := wait()
	if err == nil {
		t.Fatal("expected an error for a stream that never sends a done/error terminal event")
	}
	last := all[len(all)-1]
	if last.Type != msg.EventError {
		t.Fatalf("last event = %q, want error", last.Type)
	}
}

func TestPiMessagesResponseError(t *testing.T) {
	// pi-messages.js:282-285: a non-2xx response body is read as text and
	// wrapped in a PiMessagesResponseError; this client surfaces it as a
	// *StatusError the same way the other three clients do.
	errBody := `{"error":{"code":"rate_limited","message":"too many requests"}}`
	srv := replaySSE(t, http.StatusTooManyRequests, "application/json", []byte(errBody))
	model := piModel(srv.URL)
	client := &PiMessagesClient{}
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

// TestPiMessagesRequestShape asserts the request body's context.messages is
// the transcript's own JSON encoding verbatim (pi-messages.js:254-265: `{
// model, context, options: {temperature, maxTokens, reasoning, ...} }`), and
// that internal/msg's structs already produce pi's field names -- see
// msg.go's package doc: "Field names in JSON are pi's, not Go's."
func TestPiMessagesRequestShape(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"type\":\"done\",\"reason\":\"stop\"}\n\n"))
	}))
	defer srv.Close()

	model := piModel(srv.URL)
	transcript := []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}, Timestamp: 100}}
	client := &PiMessagesClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	temp := 0.5
	events, wait := client.Stream(ctx, model, transcript, provider.StreamOptions{Temperature: &temp, MaxTokens: 500, ThinkingLevel: provider.ThinkingHigh}, Auth{APIKey: "k"})
	_ = collectEvents(events)
	_, _ = wait()

	if gotBody["model"] != "radius-default" {
		t.Fatalf("model = %v", gotBody["model"])
	}
	ctxMap, ok := gotBody["context"].(map[string]any)
	if !ok {
		t.Fatal("context missing")
	}
	messages, ok := ctxMap["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("context.messages = %#v", ctxMap["messages"])
	}
	m0 := messages[0].(map[string]any)
	if m0["role"] != "user" {
		t.Fatalf("messages[0].role = %v, want user (pi's own field name)", m0["role"])
	}
	opts, ok := gotBody["options"].(map[string]any)
	if !ok {
		t.Fatal("options missing")
	}
	if opts["maxTokens"] != float64(500) || opts["temperature"] != 0.5 || opts["reasoning"] != "high" {
		t.Fatalf("options = %#v", opts)
	}
}
