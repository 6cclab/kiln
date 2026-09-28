package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/testkit/fauxtest"
)

const webSearchScript = `
model: faux-1
steps:
  - text: "Let me check."
  - raw_blocks:
      - {type: server_tool_use, id: srvtoolu_1, name: web_search, input: {query: "go generics"}}
      - {type: web_search_tool_result, tool_use_id: srvtoolu_1, content: [{type: web_search_result, url: "https://go.dev/blog/generics", title: "Generics in Go"}]}
  - text: "Go generics shipped in Go 1.18."
    end_turn: true
`

// TestAnthropicClientFauxRoundTrip_WebSearch drives the real AnthropicClient
// against a faux server scripted with a server_tool_use/web_search_tool_result
// pair, and asserts:
//  1. the first request declares web_search as a server tool
//  2. the parsed final message carries two anthropic ProviderBlocks in
//     order among the text blocks
//  3. sending that message back as the transcript (turn.go's own replay
//     path, exercised here directly through the client) reaches the faux
//     server with the server_tool_use/web_search_tool_result blocks
//     replayed verbatim in the request body -- the same "conversation,
//     including this partial assistant message, sent again" contract
//     Anthropic's docs describe for pause_turn, and just as necessary for
//     an ordinary completed search turn once a later turn needs the
//     context.
func TestAnthropicClientFauxRoundTrip_WebSearch(t *testing.T) {
	addr, srv := fauxtest.Start(t, webSearchScript)
	model := fauxModel(provider.ApiAnthropicMessages, "http://"+addr)
	client := &AnthropicClient{}

	transcript := []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("what's new in go generics")}},
	}
	opts := provider.StreamOptions{Tools: []provider.ToolDef{{Name: "web_search", ServerTool: webSearchServerTool}}}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, transcript, opts, Auth{APIKey: "test-key"})
	for range events {
	}
	final, err := wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}

	// (1) the first request declared the server tool.
	reqs := srv.Requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	if !strings.Contains(string(reqs[0].Body), `"web_search_20250305"`) {
		t.Fatalf("first request body = %s, want it to declare web_search_20250305", reqs[0].Body)
	}

	// (2) the parsed message carries two ProviderBlocks, in order, among
	// the text.
	var kinds []string
	for _, c := range final.Content {
		switch cv := c.(type) {
		case msg.TextContent:
			kinds = append(kinds, "text:"+cv.Text)
		case msg.ProviderBlock:
			var probe struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(cv.Raw, &probe)
			kinds = append(kinds, "block:"+probe.Type)
		}
	}
	want := []string{"text:Let me check.", "block:server_tool_use", "block:web_search_tool_result", "text:Go generics shipped in Go 1.18."}
	if len(kinds) != len(want) {
		t.Fatalf("content kinds = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("content kinds = %v, want %v", kinds, want)
		}
	}
	if final.StopReason != msg.StopStop {
		t.Fatalf("StopReason = %q, want stop (this turn used end_turn, not pause_turn)", final.StopReason)
	}

	// (3) replaying this message as history sends the blocks verbatim.
	transcript = append(transcript, msg.AssistantMessage{Role: msg.RoleAssistant, Content: final.Content})
	transcript = append(transcript, msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("thanks")}})
	events2, wait2 := client.Stream(ctx, model, transcript, provider.StreamOptions{}, Auth{APIKey: "test-key"})
	for range events2 {
	}
	if _, err := wait2(); err != nil {
		t.Fatalf("wait2: %v", err)
	}
	reqs = srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	body := string(reqs[1].Body)
	if !strings.Contains(body, `"server_tool_use"`) || !strings.Contains(body, `"srvtoolu_1"`) || !strings.Contains(body, `"go generics"`) {
		t.Fatalf("second request body missing replayed server_tool_use: %s", body)
	}
	if !strings.Contains(body, `"web_search_tool_result"`) || !strings.Contains(body, `go.dev`) {
		t.Fatalf("second request body missing replayed web_search_tool_result: %s", body)
	}
}
