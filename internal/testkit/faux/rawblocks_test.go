package faux

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestRawBlocksStreamsServerToolUseAndResult guards raw_blocks' streaming
// shape: a server_tool_use block streams its "input" via input_json_delta
// (an empty object in content_block_start, then the real query text), and
// a web_search_tool_result block arrives complete in content_block_start
// with no deltas -- matching the real Anthropic API's own behavior for
// these block types. It also guards that raw_blocks merges into the same
// turn as the text around it (a server tool resolves without a client
// round-trip, so it must not force a turn boundary the way a tool_call
// does).
func TestRawBlocksStreamsServerToolUseAndResult(t *testing.T) {
	yamlDoc := `
model: faux-1
steps:
  - text: "Let me check."
  - raw_blocks:
      - {type: server_tool_use, id: srvtoolu_1, name: web_search, input: {query: "go generics"}}
      - {type: web_search_tool_result, tool_use_id: srvtoolu_1, content: [{type: web_search_result, url: "https://go.dev", title: "Go"}]}
  - text: "Found it."
`
	_, base := startTestServer(t, yamlDoc)
	resp := postJSON(t, base+"/v1/messages", map[string]any{
		"model": "faux-1", "stream": true,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	events := readSSE(t, resp.Body)

	type block struct {
		index int
		typ   string
		id    string
		name  string
		input string // accumulated input_json_delta
		final map[string]any
	}
	blocks := map[int]*block{}
	var textParts []string
	var stopReason string
	var starts, stops int

	for _, ev := range events {
		var payload map[string]any
		if err := json.Unmarshal([]byte(ev.data), &payload); err != nil {
			t.Fatalf("bad json in event %q: %v (%s)", ev.event, err, ev.data)
		}
		switch payload["type"] {
		case "content_block_start":
			starts++
			idx := int(payload["index"].(float64))
			cb := payload["content_block"].(map[string]any)
			b := &block{index: idx, final: cb}
			b.typ, _ = cb["type"].(string)
			b.id, _ = cb["id"].(string)
			b.name, _ = cb["name"].(string)
			blocks[idx] = b
			if b.typ == "text" {
				if t, ok := cb["text"].(string); ok {
					textParts = append(textParts, t)
				}
			}
		case "content_block_delta":
			idx := int(payload["index"].(float64))
			delta := payload["delta"].(map[string]any)
			switch delta["type"] {
			case "text_delta":
				textParts = append(textParts, delta["text"].(string))
			case "input_json_delta":
				blocks[idx].input += delta["partial_json"].(string)
			}
		case "content_block_stop":
			stops++
		case "message_delta":
			d := payload["delta"].(map[string]any)
			stopReason, _ = d["stop_reason"].(string)
		}
	}

	if starts != 4 || stops != 4 {
		t.Fatalf("content_block_start/stop counts = %d/%d, want 4/4 (text, server_tool_use, web_search_tool_result, text)", starts, stops)
	}
	if stopReason != "end_turn" {
		t.Fatalf("stop_reason = %q, want end_turn (raw_blocks must not force tool_use)", stopReason)
	}

	// The server_tool_use block: empty input at content_block_start
	// (per the real API), the query streamed in via input_json_delta.
	var srv *block
	for _, b := range blocks {
		if b.typ == "server_tool_use" {
			srv = b
		}
	}
	if srv == nil {
		t.Fatalf("no server_tool_use block seen: %+v", blocks)
	}
	if srv.name != "web_search" || srv.id != "srvtoolu_1" {
		t.Fatalf("server_tool_use = %+v, want name=web_search id=srvtoolu_1", srv)
	}
	startInput, _ := srv.final["input"].(map[string]any)
	if len(startInput) != 0 {
		t.Fatalf("server_tool_use content_block_start input = %+v, want empty (streamed via delta)", startInput)
	}
	var gotInput map[string]any
	if err := json.Unmarshal([]byte(srv.input), &gotInput); err != nil {
		t.Fatalf("accumulated input_json_delta not valid JSON: %v (%s)", err, srv.input)
	}
	if gotInput["query"] != "go generics" {
		t.Fatalf("query = %v, want %q", gotInput["query"], "go generics")
	}

	// The web_search_tool_result block: complete at content_block_start,
	// no deltas contributed to it.
	var res *block
	for _, b := range blocks {
		if b.typ == "web_search_tool_result" {
			res = b
		}
	}
	if res == nil {
		t.Fatalf("no web_search_tool_result block seen: %+v", blocks)
	}
	if res.final["tool_use_id"] != "srvtoolu_1" {
		t.Fatalf("web_search_tool_result.tool_use_id = %v, want srvtoolu_1", res.final["tool_use_id"])
	}
	if res.input != "" {
		t.Fatalf("web_search_tool_result got input_json_delta = %q, want none", res.input)
	}

	joinedText := ""
	for _, p := range textParts {
		joinedText += p
	}
	if joinedText != "Let me check.Found it." {
		t.Fatalf("text = %q, want the two text steps concatenated (one turn, search in between)", joinedText)
	}
}

// TestRawBlocksStopReasonOverride guards Step.StopReason: it replaces the
// turn's inferred stop_reason, which is how a script exercises Anthropic's
// pause_turn (a long server-tool turn cut for interim delivery).
func TestRawBlocksStopReasonOverride(t *testing.T) {
	yamlDoc := `
model: faux-1
steps:
  - raw_blocks:
      - {type: server_tool_use, id: srvtoolu_1, name: web_search, input: {query: "x"}}
      - {type: web_search_tool_result, tool_use_id: srvtoolu_1, content: []}
    stop_reason: pause_turn
`
	_, base := startTestServer(t, yamlDoc)
	resp := postJSON(t, base+"/v1/messages", map[string]any{
		"model": "faux-1", "stream": true,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()
	events := readSSE(t, resp.Body)
	var stopReason string
	for _, ev := range events {
		var payload map[string]any
		if json.Unmarshal([]byte(ev.data), &payload) != nil {
			continue
		}
		if payload["type"] == "message_delta" {
			d := payload["delta"].(map[string]any)
			stopReason, _ = d["stop_reason"].(string)
		}
	}
	if stopReason != "pause_turn" {
		t.Fatalf("stop_reason = %q, want pause_turn", stopReason)
	}
}
