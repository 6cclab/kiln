package msg

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Every message in the reference sessions must decode, re-encode, and be
// semantically identical to what pi wrote.
func TestRoundTripReferenceSessions(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", "sessions", "*.jsonl"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no reference sessions: %v", err)
	}
	counts := map[Role]int{}
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		line := 0
		for sc.Scan() {
			line++
			if line == 1 {
				continue // header
			}
			var v any
			if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
				t.Fatalf("%s:%d: %v", path, line, err)
			}
			writes, ok := v.([]any)
			if !ok {
				writes = []any{v}
			}
			for _, w := range writes {
				wm := w.(map[string]any)
				if wm["kind"] != "entry" || wm["type"] != "message" {
					continue
				}
				raw, _ := json.Marshal(wm["message"])
				m, err := UnmarshalMessage(raw)
				if err != nil {
					t.Fatalf("%s:%d: %v", path, line, err)
				}
				counts[m.MessageRole()]++
				back, err := json.Marshal(m)
				if err != nil {
					t.Fatal(err)
				}
				var a, b any
				_ = json.Unmarshal(raw, &a)
				_ = json.Unmarshal(back, &b)
				if !reflect.DeepEqual(a, b) {
					t.Errorf("%s:%d: round trip differs\n got: %s\nwant: %s", path, line, back, raw)
				}
			}
		}
		f.Close()
	}
	for _, r := range []Role{RoleUser, RoleAssistant, RoleToolResult} {
		if counts[r] == 0 {
			t.Errorf("no %s messages exercised", r)
		}
	}
	t.Logf("messages round-tripped: %v", counts)
}

func TestBlocksFromString(t *testing.T) {
	var u UserMessage
	if err := json.Unmarshal([]byte(`{"role":"user","content":"hi","timestamp":1}`), &u); err != nil {
		t.Fatal(err)
	}
	if len(u.Content) != 1 || u.Content[0].(TextContent).Text != "hi" {
		t.Fatalf("got %+v", u.Content)
	}
}

func TestUnknownContentType(t *testing.T) {
	if _, err := UnmarshalContent([]byte(`{"type":"video"}`)); err == nil {
		t.Fatal("expected error")
	}
}

func TestTextOfAndToolCalls(t *testing.T) {
	b := Blocks{Text("a"), Thinking("x"), NewToolCall("1", "read", nil), Text("b")}
	if got := TextOf(b); got != "a\nb" {
		t.Fatalf("TextOf = %q", got)
	}
	if got := ToolCallsOf(b); len(got) != 1 || got[0].Name != "read" {
		t.Fatalf("ToolCallsOf = %+v", got)
	}
}

// TestProviderBlockRoundTrip guards msg.ProviderBlock's on-disk shape: an
// assistant message carrying a server_tool_use/web_search_tool_result pair
// (the shape internal/provider/api/anthropic_messages.go's stream loop
// builds) must decode, re-encode, and be byte-for-byte identical, and
// msg.ToolCallsOf must not see it -- the harness turn loop's only signal
// for "is there a tool call to execute" is ToolCallsOf, so a provider
// block silently invisible to it is exactly what keeps the turn loop from
// treating it as one.
func TestProviderBlockRoundTrip(t *testing.T) {
	raw := `{"role":"assistant","content":[` +
		`{"type":"text","text":"Let me check."},` +
		`{"type":"providerBlock","provider":"anthropic","raw":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"go generics"}}},` +
		`{"type":"providerBlock","provider":"anthropic","raw":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"web_search_result","url":"https://go.dev","title":"Go"}]}},` +
		`{"type":"text","text":"Here you go."}` +
		`],"api":"anthropic-messages","model":"m","provider":"anthropic","stopReason":"stop","timestamp":1,"usage":{"cacheRead":0,"cacheWrite":0,"cost":{"cacheRead":0,"cacheWrite":0,"input":0,"output":0,"total":0},"input":0,"output":0,"totalTokens":0}}`

	m, err := UnmarshalMessage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	am, ok := m.(AssistantMessage)
	if !ok {
		t.Fatalf("got %T, want AssistantMessage", m)
	}
	if len(am.Content) != 4 {
		t.Fatalf("Content len = %d, want 4: %+v", len(am.Content), am.Content)
	}
	pb, ok := am.Content[1].(ProviderBlock)
	if !ok || pb.Provider != "anthropic" || pb.Type != "providerBlock" {
		t.Fatalf("Content[1] = %+v, want an anthropic ProviderBlock", am.Content[1])
	}
	if !bytesContains(pb.Raw, `"server_tool_use"`) {
		t.Fatalf("ProviderBlock.Raw = %s, want it to carry the server_tool_use JSON verbatim", pb.Raw)
	}

	if got := ToolCallsOf(am.Content); len(got) != 0 {
		t.Fatalf("ToolCallsOf = %+v, want none -- a ProviderBlock must never be mistaken for a tool call", got)
	}

	back, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	_ = json.Unmarshal([]byte(raw), &a)
	_ = json.Unmarshal(back, &b)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("round trip differs\n got: %s\nwant: %s", back, raw)
	}
}

func bytesContains(raw json.RawMessage, sub string) bool {
	return len(raw) > 0 && (func() bool {
		for i := 0; i+len(sub) <= len(raw); i++ {
			if string(raw[i:i+len(sub)]) == sub {
				return true
			}
		}
		return false
	})()
}

func TestUsageAdd(t *testing.T) {
	r := 3
	u := Usage{Input: 1, Output: 2, TotalTokens: 3, Reasoning: &r}.Add(Usage{Input: 10, Output: 20, TotalTokens: 30})
	if u.Input != 11 || u.Output != 22 || u.TotalTokens != 33 || u.Reasoning == nil || *u.Reasoning != 3 {
		t.Fatalf("got %+v", u)
	}
}

func TestUsageAddServerToolUse(t *testing.T) {
	a := Usage{ServerToolUse: &ServerToolUse{WebSearchRequests: 2}, Cost: Cost{Search: 0.02, Total: 0.02}}
	b := Usage{ServerToolUse: &ServerToolUse{WebSearchRequests: 3}, Cost: Cost{Search: 0.03, Total: 0.03}}
	got := a.Add(b)
	if got.ServerToolUse == nil || got.ServerToolUse.WebSearchRequests != 5 {
		t.Fatalf("ServerToolUse = %+v, want WebSearchRequests=5", got.ServerToolUse)
	}
	if got.Cost.Search != 0.05 || got.Cost.Total != 0.05 {
		t.Fatalf("Cost = %+v, want Search=Total=0.05", got.Cost)
	}
	// One side unset must not panic and must carry the other's count.
	only := Usage{}.Add(Usage{ServerToolUse: &ServerToolUse{WebSearchRequests: 1}})
	if only.ServerToolUse == nil || only.ServerToolUse.WebSearchRequests != 1 {
		t.Fatalf("Add with one nil side = %+v", only.ServerToolUse)
	}
}
