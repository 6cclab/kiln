package api

import (
	"context"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// TestOpenAIToolCallArgsFinalized: when the stream ends, each tool call's
// accumulated argument text is parsed once more. A call cut off mid-object
// (a small max_tokens on a local server) carries the raw text in
// InvalidArgs with empty Arguments, so the harness refuses it instead of
// running the tool with no input; a complete call keeps its arguments; a
// call that sent no argument text gets {} (a valid no-argument call).
func TestOpenAIToolCallArgsFinalized(t *testing.T) {
	body := []byte(
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"read","arguments":"{\"path\": \"main"}}]},"finish_reason":null}]}` + "\n\n" +
			`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"b","type":"function","function":{"name":"read","arguments":"{\"path\":\"go.mod\"}"}}]},"finish_reason":null}]}` + "\n\n" +
			`data: {"choices":[{"delta":{"tool_calls":[{"index":2,"id":"c","type":"function","function":{"name":"todo_read","arguments":""}}]},"finish_reason":null}]}` + "\n\n" +
			`data: {"choices":[{"delta":{},"finish_reason":"length"}]}` + "\n\n" +
			"data: [DONE]\n\n")
	srv := replaySSE(t, 200, "text/event-stream", body)
	model := costedModel(provider.ApiOpenAICompletions, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := (&OpenAICompletionsClient{}).Stream(ctx, model, simpleTranscript(), provider.StreamOptions{}, Auth{APIKey: "k"})
	ended := map[string]msg.ToolCall{}
	for ev := range events {
		if ev.Type == msg.EventToolCallEnd && ev.ToolCall != nil {
			ended[ev.ToolCall.ID] = *ev.ToolCall
		}
	}
	final, err := wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	calls := map[string]msg.ToolCall{}
	for _, c := range final.Content {
		if tc, ok := c.(msg.ToolCall); ok {
			calls[tc.ID] = tc
		}
	}
	for _, got := range []map[string]msg.ToolCall{ended, calls} {
		if a := got["a"]; a.InvalidArgs != `{"path": "main` || len(a.Arguments) != 0 {
			t.Errorf("truncated call: InvalidArgs=%q Arguments=%v, want the raw text and no arguments", a.InvalidArgs, a.Arguments)
		}
		if b := got["b"]; b.InvalidArgs != "" || b.Arguments["path"] != "go.mod" {
			t.Errorf("complete call: InvalidArgs=%q Arguments=%v, want path go.mod", b.InvalidArgs, b.Arguments)
		}
		if c := got["c"]; c.InvalidArgs != "" || c.Arguments == nil || len(c.Arguments) != 0 {
			t.Errorf("no-argument call: InvalidArgs=%q Arguments=%v, want {}", c.InvalidArgs, c.Arguments)
		}
	}
}
