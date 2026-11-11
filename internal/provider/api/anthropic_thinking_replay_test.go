package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// A redacted thinking block contains opaque data, not a thinking/signature
// pair. It must survive streaming, session JSON, and the next tool-result
// request unchanged so Anthropic can continue the same assistant turn.
func TestAnthropicRedactedThinkingRoundTrip(t *testing.T) {
	const opaque = "encrypted+opaque/bytes=="
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sse := strings.Join([]string{
			`event: message_start` + "\n" + `data: {"message":{"id":"msg_1","usage":{"input_tokens":1}}}`,
			`event: content_block_start` + "\n" + fmt.Sprintf(`data: {"index":0,"content_block":{"type":"redacted_thinking","data":%q}}`, opaque),
			`event: content_block_stop` + "\n" + `data: {"index":0}`,
			`event: content_block_start` + "\n" + `data: {"index":1,"content_block":{"type":"tool_use","id":"tool_1","name":"read","input":{"path":"a.go"}}}`,
			`event: content_block_stop` + "\n" + `data: {"index":1}`,
			`event: message_delta` + "\n" + `data: {"delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}`,
		}, "\n\n") + "\n\n"
		if _, err := io.WriteString(w, sse); err != nil {
			t.Errorf("write SSE fixture: %v", err)
		}
	}))
	defer server.Close()
	model := provider.Model{ID: "claude-test", Provider: "anthropic", BaseURL: server.URL, MaxTokens: 1024}
	events, wait := (&AnthropicClient{}).Stream(context.Background(), model, nil, provider.StreamOptions{}, Auth{APIKey: "test"})
	var toolStart, toolEnd bool
	for ev := range events {
		if strings.Contains(ev.Content, opaque) || strings.Contains(ev.Delta, opaque) {
			t.Fatalf("opaque data leaked through %s event content", ev.Type)
		}
		switch ev.Type {
		case msg.EventThinkingStart, msg.EventThinkingDelta, msg.EventThinkingEnd:
			t.Fatalf("redacted block emitted a thinking display event: %s", ev.Type)
		case msg.EventToolCallStart:
			toolStart = true
			if ev.ContentIndex != 1 {
				t.Fatalf("tool start index = %d, want 1 after redacted block", ev.ContentIndex)
			}
		case msg.EventToolCallEnd:
			toolEnd = true
			if ev.ContentIndex != 1 || ev.ToolCall == nil || ev.ToolCall.ID != "tool_1" {
				t.Fatalf("tool end = %+v, want tool_1 at index 1", ev)
			}
		}
	}
	if !toolStart || !toolEnd {
		t.Fatalf("tool events missing: start=%v end=%v", toolStart, toolEnd)
	}
	answer, err := wait()
	if err != nil {
		t.Fatal(err)
	}
	var persisted msg.AssistantMessage
	stored, err := json.Marshal(answer)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(stored, &persisted); err != nil {
		t.Fatal(err)
	}
	req := buildAnthropicRequest(model, []msg.Message{persisted, msg.ToolResultMessage{ToolCallID: "tool_1", Content: msg.Blocks{msg.Text("file contents")}}}, provider.StreamOptions{}, Auth{})
	requestJSON, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Messages []struct {
			Role    string            `json:"role"`
			Content []json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(requestJSON, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Messages) != 2 || len(wire.Messages[0].Content) != 2 {
		t.Fatalf("replayed messages = %s", requestJSON)
	}
	var redacted map[string]any
	if err := json.Unmarshal(wire.Messages[0].Content[0], &redacted); err != nil {
		t.Fatal(err)
	}
	if redacted["type"] != "redacted_thinking" || redacted["data"] != opaque || len(redacted) != 2 {
		t.Fatalf("replayed redacted block = %s", wire.Messages[0].Content[0])
	}
}

// TestConvertBlocksToAnthropic_ThinkingReplay guards the wire shape of a
// replayed thinking block. Before the fix the block went out as
// {"type":"thinking","text":...}, which the API rejects with
// "messages.N.content.0.thinking.thinking: Field required"; and an unsigned
// block (qwen reasoning replayed to Claude after /model) cannot be sent at
// all. Break to verify: emit the block with Text instead of Thinking, or
// drop the signature check.
func TestConvertBlocksToAnthropic_ThinkingReplay(t *testing.T) {
	signed := msg.ThinkingContent{Type: "thinking", Thinking: "signed reasoning", ThinkingSignature: "sig-abc"}
	unsigned := msg.Thinking("qwen reasoning")
	out := convertBlocksToAnthropic(msg.Blocks{unsigned, signed, msg.Text("answer")})

	if len(out) != 2 {
		t.Fatalf("blocks = %d, want 2 (unsigned thinking dropped): %+v", len(out), out)
	}
	raw, _ := json.Marshal(out[0])
	body := string(raw)
	if !strings.Contains(body, `"thinking":"signed reasoning"`) || !strings.Contains(body, `"signature":"sig-abc"`) {
		t.Fatalf("signed thinking block = %s, want thinking+signature fields", body)
	}
	if strings.Contains(body, `"text"`) {
		t.Fatalf("signed thinking block must not carry a text field: %s", body)
	}
	if out[1].Type != "text" || out[1].Text != "answer" {
		t.Fatalf("second block = %+v, want the text block", out[1])
	}
}

// TestConvertBlocksToAnthropic_EmptySignedThinking: models that omit their
// thinking text (Opus 4.8's default) return signed blocks with thinking "".
// omitempty dropped the field on replay, and every request after the first
// tool call failed with "thinking.thinking: Field required" (seen live,
// req_011CfUSYtweBteih4b1dGEwV). The field must be present, empty.
func TestConvertBlocksToAnthropic_EmptySignedThinking(t *testing.T) {
	out := convertBlocksToAnthropic(msg.Blocks{msg.ThinkingContent{Type: "thinking", Thinking: "", ThinkingSignature: "sig-xyz"}})
	raw, _ := json.Marshal(out)
	if body := string(raw); !strings.Contains(body, `"thinking":""`) || !strings.Contains(body, `"signature":"sig-xyz"`) {
		t.Fatalf("empty signed thinking block = %s, want an explicit empty thinking field", body)
	}
	text, _ := json.Marshal(convertBlocksToAnthropic(msg.Blocks{msg.Text("hi")}))
	if strings.Contains(string(text), `"thinking"`) {
		t.Fatalf("a text block must not carry a thinking field: %s", text)
	}
}

// TestToolResultWithImageIsSentAsBlocks guards the tool_result shape: a
// text-only result is a plain string, a result with an image is a block
// list carrying the image, so a read of a PNG reaches the model. Break to
// verify: always use msg.TextOf for the content.
func TestToolResultWithImageIsSentAsBlocks(t *testing.T) {
	text := msg.ToolResultMessage{ToolCallID: "t1", Content: msg.Blocks{msg.Text("hello")}}
	img := msg.ToolResultMessage{ToolCallID: "t2", Content: msg.Blocks{msg.Text("Read image file [image/png]"), msg.ImageContent{Type: "image", MimeType: "image/png", Data: "AAAA"}}}
	req := buildAnthropicRequest(provider.Model{ID: "m", MaxTokens: 1024}, []msg.Message{msg.UserMessage{Content: msg.Blocks{msg.Text("hi")}}, text, img}, provider.StreamOptions{}, Auth{})
	raw, _ := json.Marshal(req.Messages)
	body := string(raw)
	if !strings.Contains(body, `"content":"hello"`) {
		t.Fatalf("text-only tool_result must stay a string: %s", body)
	}
	if !strings.Contains(body, `"type":"image"`) || !strings.Contains(body, `"data":"AAAA"`) {
		t.Fatalf("image tool_result must carry the image block: %s", body)
	}
}

// TestBuildAnthropicRequest_CachesConversation: the conversation carries
// cache breakpoints on the last two user-side messages, and the request
// never exceeds the API's four breakpoints (tools, system, two messages),
// OAuth identity block included. Only tools and system used to be marked,
// so every request paid full price for the whole history again
// (qa/findings *conversation-not-prompt-cached).
func TestBuildAnthropicRequest_CachesConversation(t *testing.T) {
	transcript := []msg.Message{
		msg.UserMessage{Content: msg.Blocks{msg.Text("build it")}},
		msg.AssistantMessage{Content: msg.Blocks{msg.ToolCall{Type: "toolCall", ID: "t1", Name: "bash", Arguments: map[string]any{"command": "ls"}}}},
		msg.ToolResultMessage{ToolCallID: "t1", Content: msg.Blocks{msg.Text("a b c")}},
		msg.AssistantMessage{Content: msg.Blocks{msg.ToolCall{Type: "toolCall", ID: "t2", Name: "bash", Arguments: map[string]any{"command": "pwd"}}}},
		msg.ToolResultMessage{ToolCallID: "t2", Content: msg.Blocks{msg.Text("/x")}},
	}
	opts := provider.StreamOptions{SystemPrompt: "sys", Tools: []provider.ToolDef{{Name: "bash", Description: "run"}}}
	for _, oauth := range []bool{false, true} {
		req := buildAnthropicRequest(provider.Model{ID: "m", MaxTokens: 1024}, transcript, opts, Auth{IsOAuth: oauth})
		raw, _ := json.Marshal(req)
		if n := strings.Count(string(raw), `"cache_control"`); n > 4 {
			t.Errorf("oauth=%v: %d cache breakpoints, the API allows 4", oauth, n)
		}
		marked := func(i int) bool {
			b, _ := req.Messages[i].Content.([]anthropicContentBlock)
			return len(b) > 0 && b[len(b)-1].CacheCtrl != nil
		}
		if !marked(4) || !marked(2) {
			t.Errorf("oauth=%v: want breakpoints on the last two tool results (messages 2 and 4)", oauth)
		}
		if marked(0) || marked(1) || marked(3) {
			t.Errorf("oauth=%v: breakpoint on an older or assistant message", oauth)
		}
	}
}
