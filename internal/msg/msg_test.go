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

func TestUsageAdd(t *testing.T) {
	r := 3
	u := Usage{Input: 1, Output: 2, TotalTokens: 3, Reasoning: &r}.Add(Usage{Input: 10, Output: 20, TotalTokens: 30})
	if u.Input != 11 || u.Output != 22 || u.TotalTokens != 33 || u.Reasoning == nil || *u.Reasoning != 3 {
		t.Fatalf("got %+v", u)
	}
}
