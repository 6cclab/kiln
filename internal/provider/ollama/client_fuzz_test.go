package ollama

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"runtime/debug"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider/api"
)

type bodyTransport []byte

func (b bodyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	_, _ = io.Copy(io.Discard, r.Body)
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"application/x-ndjson"}},
		Body:       io.NopCloser(bytes.NewReader(b)),
		Request:    r,
	}, nil
}

// FuzzNativeStream feeds arbitrary bytes to the native /api/chat decoder
// as a reply body, on the calling goroutine so a panic fails the input
// instead of ending the fuzzing process. The request carries an assistant
// tool call and an empty tool result, the shape of a turn's second
// request.
func FuzzNativeStream(f *testing.F) {
	seeds := []string{
		`{"message":{"role":"assistant","content":"hi"},"done":true,"done_reason":"stop"}` + "\n",
		`{"message":{"role":"assistant","content":"","thinking":"Let me"},"done":false}` + "\n" +
			`{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","function":{"index":0,"name":"bash","arguments":{"command":"ls"}}}]},"done":false}` + "\n" +
			`{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}` + "\n",
		`{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"index":-3,"name":"x","arguments":null}}]},"done":true}`,
		`{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"a","arguments":{}}},{"function":{"name":"b","arguments":{"k":[1,2]}}}]},"done":true,"done_reason":"length"}` + "\n",
		`{"error":"model runner has unexpectedly stopped"}` + "\n",
		`{"done":true}` + "\n",
		`{"message":null,"done":true,"done_reason":"load"}`,
		"\n\n{\"message\":{\"content\":\"partial\"",
		`{"message":{"role":"assistant","content":"a"},"done":false}` + "\n" + `{"message":{"role":"assistant","thinking":"b"},"done":false}` + "\n" + `{"done":true,"done_reason":"unload"}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	model := toModel("m:latest", "http://ollama.invalid", 8192, []string{"completion", "tools"})
	call := msg.NewToolCall("call_1", "skill", map[string]any{"name": "x"})
	transcript := []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("use the skill")}},
		msg.AssistantMessage{Role: msg.RoleAssistant, Content: msg.Blocks{msg.Thinking("t"), call}},
		msg.ToolResultMessage{Role: msg.RoleToolResult, Content: msg.Blocks{}, ToolCallID: "call_1", ToolName: "skill"},
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		c := &nativeClient{HTTPClient: &http.Client{Transport: bodyTransport(body)}}
		events := make(chan msg.StreamEvent, 16)
		drained := make(chan struct{})
		go func() {
			for range events {
			}
			close(drained)
		}()
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic: %v\n%s", r, debug.Stack())
				}
			}()
			_, _ = c.run(context.Background(), model, transcript, streamOptsWithTool(), api.Auth{}, events)
		}()
		close(events)
		<-drained
	})
}
