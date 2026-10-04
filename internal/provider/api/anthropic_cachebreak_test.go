package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

func cacheBreak(text string) msg.TextContent {
	t := msg.Text(text)
	t.CacheBreak = true
	return t
}

// A text block that asks for a cache breakpoint gets one while the request
// stays within the API's four, and the request never carries more.
func TestRequestedCacheBreaks(t *testing.T) {
	model := provider.Model{ID: "m", MaxTokens: 1024}
	user := msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("head"), cacheBreak("stable"), msg.Text("action")}}
	req := buildAnthropicRequest(model, []msg.Message{user}, provider.StreamOptions{SystemPrompt: "sys"}, Auth{})
	blocks := req.Messages[0].Content.([]anthropicContentBlock)
	if blocks[1].CacheCtrl == nil || blocks[1].CacheCtrl == requestedCacheBreak || blocks[1].CacheCtrl.Type != "ephemeral" {
		t.Fatalf("requested breakpoint = %+v, want a real one", blocks[1].CacheCtrl)
	}
	if blocks[0].CacheCtrl != nil {
		t.Fatal("a block that asked for nothing got a breakpoint")
	}

	// Tools, system and two user messages already use the four: a request
	// for more is dropped, not sent.
	tools := []provider.ToolDef{{Name: "bash", Parameters: json.RawMessage(`{"type":"object"}`)}}
	transcript := []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{cacheBreak("old"), msg.Text("one")}},
		msg.AssistantMessage{Role: msg.RoleAssistant, Content: msg.Blocks{msg.Text("ok")}},
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("two")}},
	}
	req = buildAnthropicRequest(model, transcript, provider.StreamOptions{SystemPrompt: "sys", Tools: tools}, Auth{})
	body, _ := json.Marshal(req)
	if n := strings.Count(string(body), `"cache_control"`); n != maxCacheBreakpoints {
		t.Fatalf("%d cache_control blocks, want exactly %d: %s", n, maxCacheBreakpoints, body)
	}
}
