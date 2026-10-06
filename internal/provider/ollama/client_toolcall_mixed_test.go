package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

func streamOptsWithTool() provider.StreamOptions {
	return provider.StreamOptions{Tools: []provider.ToolDef{
		{Name: "bash", Description: "Run", Parameters: json.RawMessage(`{"type":"object"}`)},
		{Name: "read", Description: "Read", Parameters: json.RawMessage(`{"type":"object"}`)},
	}}
}

// streamLines serves lines as one /api/chat reply and returns the final
// message and the tool calls the stream ended, in event order.
func streamLines(t *testing.T, lines ...map[string]any) (*msg.AssistantMessage, []msg.ToolCall, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeNDJSON(w, lines...)
	}))
	defer srv.Close()
	model := toModel("m:latest", srv.URL, 8192, []string{"completion", "tools"})
	p := New(Options{URL: srv.URL})
	user := msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("go")}}
	events, wait := p.Stream(context.Background(), model, []msg.Message{user}, streamOptsWithTool())
	var ended []msg.ToolCall
	for ev := range events {
		if ev.Type == msg.EventToolCallEnd && ev.ToolCall != nil {
			ended = append(ended, *ev.ToolCall)
		}
	}
	final, err := wait()
	return final, ended, err
}

func toolCallLine(index int, id, name string, args map[string]any) map[string]any {
	return map[string]any{
		"message": map[string]any{"role": "assistant", "content": "", "tool_calls": []map[string]any{{
			"id":       id,
			"function": map[string]any{"index": index, "name": name, "arguments": args},
		}}},
		"done": false,
	}
}

var doneLine = map[string]any{"message": map[string]any{"role": "assistant", "content": ""}, "done": true, "done_reason": "stop", "prompt_eval_count": 10, "eval_count": 5}

// Thinking (or text) before a tool call put the call at content position
// 1 while its Ollama index stayed 0, and the end-of-stream loop used the
// index as the position: Content[0] was the thinking block, and the type
// assertion panicked on the stream goroutine ("interface conversion:
// msg.Content is msg.ThinkingContent, not msg.ToolCall"), killing kiln
// with the terminal in raw mode. qwen3 thinks before it calls a tool.
func TestNativeToolCallAfterThinkingAndText(t *testing.T) {
	final, ended, err := streamLines(t,
		map[string]any{"message": map[string]any{"role": "assistant", "content": "", "thinking": "Let me look."}, "done": false},
		map[string]any{"message": map[string]any{"role": "assistant", "content": "Listing files."}, "done": false},
		toolCallLine(0, "call_1", "bash", map[string]any{"command": "ls"}),
		toolCallLine(1, "call_2", "read", map[string]any{"path": "a.txt"}),
		doneLine,
	)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if final.StopReason != msg.StopToolUse {
		t.Errorf("StopReason = %q, want toolUse", final.StopReason)
	}
	if len(final.Content) != 4 {
		t.Fatalf("content = %+v, want thinking, text and two tool calls", final.Content)
	}
	if _, ok := final.Content[0].(msg.ThinkingContent); !ok {
		t.Errorf("content[0] = %T, want thinking", final.Content[0])
	}
	if len(ended) != 2 || ended[0].ID != "call_1" || ended[0].Name != "bash" || ended[1].ID != "call_2" || ended[1].Name != "read" {
		t.Errorf("ended tool calls = %+v, want call_1 bash then call_2 read", ended)
	}
}

// An index that is no content position at all (negative, or past the
// content) is only a key.
func TestNativeToolCallOddIndexes(t *testing.T) {
	for _, index := range []int{-3, 7} {
		final, ended, err := streamLines(t,
			toolCallLine(index, "call_x", "bash", map[string]any{"command": "ls"}),
			doneLine,
		)
		if err != nil {
			t.Fatalf("index %d: %v", index, err)
		}
		if len(ended) != 1 || ended[0].ID != "call_x" || len(final.Content) != 1 {
			t.Errorf("index %d: ended %+v, content %+v", index, ended, final.Content)
		}
	}
}
