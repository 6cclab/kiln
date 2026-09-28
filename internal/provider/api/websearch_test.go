package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// webSearchServerTool mirrors internal/tools.webSearchServerToolJSON,
// duplicated here rather than imported (internal/provider/api must not
// depend on internal/tools) so these tests exercise the same declaration a
// real session sends.
var webSearchServerTool = json.RawMessage(`{"type":"web_search_20250305","name":"web_search","max_uses":5}`)

// TestBuildAnthropicRequest_ServerToolDeclared guards that a ToolDef
// carrying ServerTool is sent verbatim in the Anthropic request's tools
// array, in place of a function schema.
func TestBuildAnthropicRequest_ServerToolDeclared(t *testing.T) {
	req := buildAnthropicRequest(
		provider.Model{ID: "m", MaxTokens: 1024},
		[]msg.Message{msg.UserMessage{Content: msg.Blocks{msg.Text("hi")}}},
		provider.StreamOptions{Tools: []provider.ToolDef{{Name: "web_search", ServerTool: webSearchServerTool}}},
		Auth{},
	)
	raw, err := json.Marshal(req.Tools)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if !strings.Contains(body, `"type":"web_search_20250305"`) || !strings.Contains(body, `"max_uses":5`) {
		t.Fatalf("tools = %s, want the server tool declaration verbatim", body)
	}
	if strings.Contains(body, `"input_schema"`) {
		t.Fatalf("tools = %s, must not carry a function schema for a server tool", body)
	}
}

// TestBuildAnthropicRequest_ServerToolAlongsideFunctionTool guards that a
// server tool and an ordinary function tool coexist correctly, each in
// its own wire shape, when both are declared in the same request.
func TestBuildAnthropicRequest_ServerToolAlongsideFunctionTool(t *testing.T) {
	req := buildAnthropicRequest(
		provider.Model{ID: "m", MaxTokens: 1024},
		[]msg.Message{msg.UserMessage{Content: msg.Blocks{msg.Text("hi")}}},
		provider.StreamOptions{Tools: []provider.ToolDef{
			{Name: "read", Parameters: json.RawMessage(`{"type":"object"}`)},
			{Name: "web_search", ServerTool: webSearchServerTool},
		}},
		Auth{},
	)
	if len(req.Tools) != 2 {
		t.Fatalf("Tools len = %d, want 2", len(req.Tools))
	}
	raw, _ := json.Marshal(req.Tools)
	body := string(raw)
	if !strings.Contains(body, `"name":"read"`) {
		t.Fatalf("tools = %s, want the read function tool intact", body)
	}
	if !strings.Contains(body, `"web_search_20250305"`) {
		t.Fatalf("tools = %s, want the web_search server tool intact", body)
	}
}

// TestConvertBlocksToAnthropic_ProviderBlockReplay guards that a
// msg.ProviderBlock recorded for provider "anthropic" replays verbatim
// into the request, in position among the surrounding blocks, and that a
// ProviderBlock recorded for a different provider is dropped rather than
// sent (a session that switched models mid-conversation must not replay
// another provider's native block to Anthropic).
func TestConvertBlocksToAnthropic_ProviderBlockReplay(t *testing.T) {
	serverToolUse := msg.ProviderBlock{
		Provider: "anthropic", Type: "providerBlock",
		Raw: json.RawMessage(`{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"go generics"}}`),
	}
	resultBlock := msg.ProviderBlock{
		Provider: "anthropic", Type: "providerBlock",
		Raw: json.RawMessage(`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"web_search_result","url":"https://go.dev","title":"Go"}]}`),
	}
	fromOther := msg.ProviderBlock{Provider: "openai", Type: "providerBlock", Raw: json.RawMessage(`{"type":"something_else"}`)}

	out := convertBlocksToAnthropic(msg.Blocks{msg.Text("before"), serverToolUse, resultBlock, fromOther, msg.Text("after")})
	if len(out) != 4 {
		t.Fatalf("blocks = %d, want 4 (the other-provider block dropped): %+v", len(out), out)
	}
	if out[0].Type != "text" || out[0].Text != "before" {
		t.Fatalf("out[0] = %+v, want the leading text block", out[0])
	}
	raw1, _ := json.Marshal(out[1])
	if !strings.Contains(string(raw1), `"server_tool_use"`) || !strings.Contains(string(raw1), `"go generics"`) {
		t.Fatalf("out[1] = %s, want the server_tool_use block verbatim", raw1)
	}
	raw2, _ := json.Marshal(out[2])
	if !strings.Contains(string(raw2), `"web_search_tool_result"`) || !strings.Contains(string(raw2), `go.dev`) {
		t.Fatalf("out[2] = %s, want the web_search_tool_result block verbatim", raw2)
	}
	if out[3].Type != "text" || out[3].Text != "after" {
		t.Fatalf("out[3] = %+v, want the trailing text block", out[3])
	}
}

// TestMapAnthropicStopReason_PauseTurn guards that pause_turn maps to its
// own msg.StopPause, distinct from end_turn/stop_sequence's msg.StopStop:
// internal/harness/turn.go's drive() only re-requests on StopPause, so
// folding it into StopStop (as it was before this feature) would silently
// end a long server-tool turn instead of continuing it.
func TestMapAnthropicStopReason_PauseTurn(t *testing.T) {
	reason, errMsg := mapAnthropicStopReason("pause_turn")
	if reason != msg.StopPause || errMsg != "" {
		t.Fatalf("mapAnthropicStopReason(pause_turn) = (%q, %q), want (%q, \"\")", reason, errMsg, msg.StopPause)
	}
	for _, r := range []string{"end_turn", "stop_sequence"} {
		if got, _ := mapAnthropicStopReason(r); got != msg.StopStop {
			t.Fatalf("mapAnthropicStopReason(%s) = %q, want %q", r, got, msg.StopStop)
		}
	}
}

// TestComputeAnthropicCost_ServerToolUse guards the $10/1000-searches
// pricing and that it folds into Cost.Total alongside the token costs.
func TestComputeAnthropicCost_ServerToolUse(t *testing.T) {
	u := msg.Usage{Input: 0, Output: 0, ServerToolUse: &msg.ServerToolUse{WebSearchRequests: 3}}
	computeAnthropicCost(provider.Model{}, &u)
	want := 3.0 / 1000 * 10
	if u.Cost.Search != want {
		t.Fatalf("Cost.Search = %v, want %v", u.Cost.Search, want)
	}
	if u.Cost.Total != want {
		t.Fatalf("Cost.Total = %v, want %v", u.Cost.Total, want)
	}
}
