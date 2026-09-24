package api

// Conformance tests for MistralConversationsClient, replayed against
// hand-authored SSE fixtures under testdata/mistral/*.sse (there is no faux
// recording path for this client -- faux only speaks anthropic-messages and
// openai-completions shapes -- so every fixture here is hand-authored plain
// `data: <json>` SSE, matching testdata/openai/text_only.sse's framing and
// ending with `data: [DONE]`).
//
// See mistral_conversations.go's package doc comment for the "conversations
// vs actual chat-completions wire shape" naming surprise this client's own
// source carries.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

func mistralCostedModel(baseURL string) provider.Model {
	return costedModel(provider.ApiMistralConversations, baseURL)
}

// ============================================================================
// Streaming fixtures
// ============================================================================

func TestFixtureMistralTextOnly(t *testing.T) {
	body := loadFixture(t, "mistral", "text_only", nil)
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := mistralCostedModel(srv.URL)
	client := &MistralConversationsClient{}
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

	if msg.TextOf(final.Content) != "Hello from Mistral." {
		t.Fatalf("text = %q", msg.TextOf(final.Content))
	}
	if final.StopReason != msg.StopStop {
		t.Fatalf("StopReason = %q, want stop", final.StopReason)
	}
	if final.RawStopReason != "stop" {
		t.Fatalf("RawStopReason = %q, want stop", final.RawStopReason)
	}
	if final.ResponseID != "cmpl-mistral-1" {
		t.Fatalf("ResponseID = %q, want cmpl-mistral-1 (first non-empty chunk.id, kept thereafter)", final.ResponseID)
	}
	if final.Usage.Input != 120 || final.Usage.Output != 40 {
		t.Fatalf("usage = %+v, want input=120 output=40", final.Usage)
	}
	assertCost(t, model, final.Usage)
}

func TestFixtureMistralReasoningPromptMode(t *testing.T) {
	body := loadFixture(t, "mistral", "reasoning_prompt_mode", nil)
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := mistralCostedModel(srv.URL)
	client := &MistralConversationsClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	transcript := []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("what is 2+2")}}}
	events, wait := client.Stream(ctx, model, transcript, provider.StreamOptions{}, Auth{APIKey: "k"})
	all := collectEvents(events)
	final, err := wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	assertPartialIdentity(t, all, final)
	assertTextMonotonic(t, all)

	// Single-current-block reopen logic: a thinking block first, then a
	// distinct text block, at distinct content indices.
	if len(final.Content) != 2 {
		t.Fatalf("content blocks = %d, want 2 (thinking, text)", len(final.Content))
	}
	thinking, ok := final.Content[0].(msg.ThinkingContent)
	if !ok || thinking.Thinking != "Let me think about this carefully." {
		t.Fatalf("content[0] = %+v, want thinking %q", final.Content[0], "Let me think about this carefully.")
	}
	text, ok := final.Content[1].(msg.TextContent)
	if !ok || text.Text != "The answer is 4." {
		t.Fatalf("content[1] = %+v, want text %q", final.Content[1], "The answer is 4.")
	}

	var thinkingStartIdx, textStartIdx = -1, -1
	for _, e := range all {
		if e.Type == msg.EventThinkingStart && thinkingStartIdx == -1 {
			thinkingStartIdx = e.ContentIndex
		}
		if e.Type == msg.EventTextStart && textStartIdx == -1 {
			textStartIdx = e.ContentIndex
		}
	}
	if thinkingStartIdx != 0 || textStartIdx != 1 {
		t.Fatalf("thinking_start at %d, text_start at %d, want 0 then 1 (in order)", thinkingStartIdx, textStartIdx)
	}
}

func TestFixtureMistralFunctionCall(t *testing.T) {
	body := loadFixture(t, "mistral", "function_call", nil)
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := mistralCostedModel(srv.URL)
	client := &MistralConversationsClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	transcript := []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("check the weather")}}}
	events, wait := client.Stream(ctx, model, transcript, provider.StreamOptions{}, Auth{APIKey: "k"})
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
		t.Fatalf("tool call delta events = %d, want >=2 (partial JSON split over multiple chunks)", deltaCount)
	}

	calls := msg.ToolCallsOf(final.Content)
	if len(calls) != 1 || calls[0].Name != "search" {
		t.Fatalf("tool calls = %+v", calls)
	}
	if calls[0].Arguments["query"] != "weather in sf" || calls[0].Arguments["limit"] != float64(5) {
		t.Fatalf("tool call args = %+v", calls[0].Arguments)
	}
	if final.StopReason != msg.StopToolUse {
		t.Fatalf("StopReason = %q, want toolUse", final.StopReason)
	}
	if final.RawStopReason != "tool_calls" {
		t.Fatalf("RawStopReason = %q, want tool_calls", final.RawStopReason)
	}
}

func TestFixtureMistralUsageMultiChunk(t *testing.T) {
	body := loadFixture(t, "mistral", "usage_multi_chunk", nil)
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := mistralCostedModel(srv.URL)
	client := &MistralConversationsClient{}
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

	// usage arrived on a non-final chunk, using the "prompt_tokens_details.
	// cached_tokens" alternate spelling -- verify usage handling doesn't
	// require usage to be on the terminal chunk, and that the alternate
	// cached-token spelling was picked up.
	if final.Usage.CacheRead != 50 {
		t.Fatalf("Usage.CacheRead = %d, want 50 (from prompt_tokens_details.cached_tokens on a non-final chunk)", final.Usage.CacheRead)
	}
	if final.Usage.Input != 150 { // 200 prompt - 50 cached
		t.Fatalf("Usage.Input = %d, want 150 (200 prompt_tokens - 50 cached)", final.Usage.Input)
	}
	assertCost(t, model, final.Usage)
}

func TestFixtureMistralTruncated(t *testing.T) {
	body := loadFixture(t, "mistral", "truncated", nil)
	srv := replaySSE(t, http.StatusOK, "text/event-stream", body)
	model := mistralCostedModel(srv.URL)
	client := &MistralConversationsClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	transcript := []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}
	events, wait := client.Stream(ctx, model, transcript, provider.StreamOptions{}, Auth{APIKey: "k"})
	all := collectEvents(events)
	_, err := wait()
	if err == nil {
		t.Fatal("expected an error for a stream that ends mid-event with no finish_reason and no [DONE]")
	}
	last := all[len(all)-1]
	if last.Type != msg.EventError {
		t.Fatalf("last event = %q, want error", last.Type)
	}
	if last.Reason != msg.StopError {
		t.Fatalf("error reason = %q, want error", last.Reason)
	}
}

func TestFixtureMistralError429(t *testing.T) {
	testMistralErrorFixture(t, "error_429", 429, "rate_limit_error", true)
}

func TestFixtureMistralError529(t *testing.T) {
	testMistralErrorFixture(t, "error_529", 529, "overloaded_error", true)
}

func testMistralErrorFixture(t *testing.T, name string, status int, errType string, wantRetriable bool) {
	t.Helper()
	body := loadFixture(t, "mistral", name, nil)
	srv := replaySSE(t, status, "application/json", body)
	model := mistralCostedModel(srv.URL)
	client := &MistralConversationsClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	transcript := []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}
	events, wait := client.Stream(ctx, model, transcript, provider.StreamOptions{}, Auth{APIKey: "k"})
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
// Tool-call id normalization (no HTTP)
// ============================================================================

func TestMistralToolCallIDNormalizer(t *testing.T) {
	n := newMistralToolCallIDNormalizer()

	// Already exactly 9 alphanumeric chars: passed through unchanged.
	conforming := "abc123XYZ"
	if len(conforming) != mistralToolCallIDLength {
		t.Fatalf("test setup: %q is not %d chars", conforming, mistralToolCallIDLength)
	}
	if got := n.normalize(conforming); got != conforming {
		t.Fatalf("normalize(%q) = %q, want unchanged passthrough", conforming, got)
	}

	// Non-conforming length/chars: hash-derived to 9 alphanumeric chars.
	nonConforming := "tool-call-id-with-dashes-and-a-very-long-name"
	got := n.normalize(nonConforming)
	if len(got) != mistralToolCallIDLength {
		t.Fatalf("normalize(%q) = %q, want length %d", nonConforming, got, mistralToolCallIDLength)
	}
	for _, r := range got {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			t.Fatalf("normalize(%q) = %q, contains non-alphanumeric char %q", nonConforming, got, r)
		}
	}
	// Idempotent for the same source id.
	if got2 := n.normalize(nonConforming); got2 != got {
		t.Fatalf("normalize(%q) called twice returned %q then %q, want stable", nonConforming, got, got2)
	}

	// Collision case: force two different source ids to hash to the same
	// candidate at attempt 0, and verify the second gets a different
	// candidate via the attempt-increment retry. We override the
	// package-level mistralShortHash var rather than relying on an actual
	// sha256 collision (astronomically unlikely to construct by hand).
	origHash := mistralShortHash
	defer func() { mistralShortHash = origHash }()
	mistralShortHash = func(seed string) string {
		if seed == "sourceA" || seed == "sourceB" {
			return "collision000000" // both attempt-0 seeds collide
		}
		return "seed" + seed + "unique000000" // attempt>=1 seeds (suffixed ":1", ...) differ
	}

	n2 := newMistralToolCallIDNormalizer()
	candidateA := n2.normalize("sourceA")
	candidateB := n2.normalize("sourceB")
	if candidateA == candidateB {
		t.Fatalf("normalize(sourceA) = %q collided with normalize(sourceB) = %q; the attempt-increment retry should have produced a different candidate for the second id", candidateA, candidateB)
	}
}

// ============================================================================
// Request-body shape tests (no HTTP; buildMistralRequest directly)
// ============================================================================

// usesReasoningEffort / usesPromptModeReasoning, mistral-conversations.js:708-716:
//
//	function usesReasoningEffort(model) {
//	    return model.id === "mistral-small-2603" || model.id === "mistral-small-latest" ||
//	        model.id.startsWith("mistral-medium-") || model.id === "zai-glm-5-2";
//	}
//	function usesPromptModeReasoning(model) {
//	    return model.reasoning && !usesReasoningEffort(model);
//	}
func TestMistralRequestReasoningEffortAllowlist(t *testing.T) {
	model := provider.Model{ID: "mistral-medium-2512", Reasoning: true, Input: []string{"text"}}
	req := buildMistralRequest(model, nil, provider.StreamOptions{ThinkingLevel: provider.ThinkingHigh}, newMistralToolCallIDNormalizer().normalize)
	if req.ReasoningEffort == "" {
		t.Fatal("reasoning_effort not set for a usesReasoningEffort model id")
	}
	if req.PromptMode != "" {
		t.Fatalf("prompt_mode = %q, want unset for a usesReasoningEffort model", req.PromptMode)
	}
}

func TestMistralRequestPromptModeReasoning(t *testing.T) {
	// Any reasoning model NOT in the usesReasoningEffort allowlist.
	model := provider.Model{ID: "some-other-reasoning-model", Reasoning: true, Input: []string{"text"}}
	req := buildMistralRequest(model, nil, provider.StreamOptions{ThinkingLevel: provider.ThinkingHigh}, newMistralToolCallIDNormalizer().normalize)
	if req.PromptMode != "reasoning" {
		t.Fatalf("prompt_mode = %q, want %q", req.PromptMode, "reasoning")
	}
	if req.ReasoningEffort != "" {
		t.Fatalf("reasoning_effort = %q, want unset for a non-allowlisted reasoning model", req.ReasoningEffort)
	}
}

func TestMistralRequestReasoningEffortFallbackIsLiteralHigh(t *testing.T) {
	// mapReasoningEffort's fallback (mistral-conversations.js:717-719) is the
	// string literal "high", not the raw thinking level -- unlike every
	// other client in this package.
	model := provider.Model{ID: "mistral-small-latest", Reasoning: true, Input: []string{"text"}}
	req := buildMistralRequest(model, nil, provider.StreamOptions{ThinkingLevel: provider.ThinkingLow}, newMistralToolCallIDNormalizer().normalize)
	if req.ReasoningEffort != "high" {
		t.Fatalf("reasoning_effort = %q, want literal %q (pi's fallback, even though ThinkingLevel was %q)", req.ReasoningEffort, "high", provider.ThinkingLow)
	}
}

// TestMistralRequestImageURLIsBareString asserts the image_url content
// part's JSON value is a bare string, not an {"url": "..."} object -- unlike
// openai_completions.go's openAIImageURL, which wraps it. Confirmed by
// reading mistral-conversations.js:618 (`{ type: "image_url", imageUrl:
// ... }`, wire-remapped straight to image_url with no nested object).
func TestMistralRequestImageURLIsBareString(t *testing.T) {
	model := provider.Model{ID: "pixtral-large-latest", Input: []string{"text", "image"}}
	transcript := []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{
			msg.Text("what is this?"),
			msg.Image("image/png", "ZmFrZS1wbmc="),
		}},
	}
	req := buildMistralRequest(model, transcript, provider.StreamOptions{}, newMistralToolCallIDNormalizer().normalize)
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type     string          `json:"type"`
				ImageURL json.RawMessage `json:"image_url"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal wire body: %v", err)
	}
	var found bool
	for _, m := range decoded.Messages {
		if m.Role != "user" {
			continue
		}
		for _, part := range m.Content {
			if part.Type != "image_url" {
				continue
			}
			found = true
			raw := strings.TrimSpace(string(part.ImageURL))
			if !strings.HasPrefix(raw, `"`) {
				t.Fatalf("image_url wire value = %s, want a bare JSON string (got an object/other shape)", raw)
			}
			var s string
			if err := json.Unmarshal(part.ImageURL, &s); err != nil {
				t.Fatalf("image_url did not decode as a plain string: %v (raw: %s)", err, raw)
			}
			if !strings.HasPrefix(s, "data:image/png;base64,") {
				t.Fatalf("image_url string = %q, want a data: URL", s)
			}
		}
	}
	if !found {
		t.Fatal("no image_url content part found in the built request")
	}
}
