package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

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
