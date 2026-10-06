package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// Generation requests have no whole-request deadline: http.Client.Timeout
// covers reading the body, so any value there cuts off a slow local model
// mid-answer (a 3-minute one killed compactions on qwen at exactly 180s).
// Discovery calls keep their per-call bound.
func TestStreamClientHasNoTotalDeadline(t *testing.T) {
	for name, opts := range map[string]Options{
		"default":          {},
		"custom transport": {HTTPClient: &http.Client{Transport: http.DefaultTransport}},
	} {
		p := New(opts)
		if got := p.client.HTTPClient.Timeout; got != 0 {
			t.Errorf("%s: stream client Timeout = %s, want 0 (no total deadline)", name, got)
		}
		if got := opts.client().Timeout; got != discoveryTimeout {
			t.Errorf("%s: discovery client Timeout = %s, want %s", name, got, discoveryTimeout)
		}
	}
}

// readNDJSONRequest decodes one native /api/chat request body, for
// assertions on what this client actually sent.
func readNDJSONRequest(t *testing.T, r *http.Request) nativeChatRequest {
	t.Helper()
	var req nativeChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return req
}

func writeNDJSON(w http.ResponseWriter, lines ...map[string]any) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	fl, _ := w.(http.Flusher)
	for _, l := range lines {
		b, _ := json.Marshal(l)
		w.Write(b)
		w.Write([]byte("\n"))
		if fl != nil {
			fl.Flush()
		}
	}
}

// --- the defect this fix closes ---

// TestNativeRequestSendsNumCtxEqualToContextWindow is the defect-specific
// test: every request this client sends carries options.num_ctx equal to
// the model's ContextWindow, so the window kiln budgets for
// (internal/budget.TierForWindow, fed Model.ContextWindow) is by
// construction the window Ollama is asked to serve.
//
// This fails against origin/main's Provider.Stream, which drove the
// openai-completions client at {base}/v1 and never sent num_ctx at all
// (verified live against Ollama 0.34.0: options.num_ctx over /v1 is
// silently ignored, and /v1's own request shape -- openai_completions.go's
// openAIRequest -- has no num_ctx field to send it in even if the
// transport honored it). A Stream call built the old way could not pass
// this assertion by construction, independent of what the test server
// does with the field.
func TestNativeRequestSendsNumCtxEqualToContextWindow(t *testing.T) {
	var gotPath string
	var gotNumCtx int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		req := readNDJSONRequest(t, r)
		if req.Options != nil {
			gotNumCtx = req.Options.NumCtx
		}
		writeNDJSON(w, map[string]any{
			"message": map[string]any{"role": "assistant", "content": "ok"},
			"done":    true, "done_reason": "stop",
			"prompt_eval_count": 5, "eval_count": 1,
		})
	}))
	defer srv.Close()

	model := toModel("qwen3:8b", srv.URL, 32768, []string{"completion", "tools"})
	p := New(Options{URL: srv.URL})
	user := msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}
	events, wait := p.Stream(context.Background(), model, []msg.Message{user}, provider.StreamOptions{})
	for range events {
	}
	if _, err := wait(); err != nil {
		t.Fatalf("stream: %v", err)
	}

	if gotPath != "/api/chat" {
		t.Fatalf("request path = %q, want /api/chat (not /v1/chat/completions)", gotPath)
	}
	if gotNumCtx != 32768 {
		t.Fatalf("options.num_ctx = %d, want 32768 (model.ContextWindow)", gotNumCtx)
	}
}

// --- parity: text streaming ---

func TestNativeTextStreaming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeNDJSON(w,
			map[string]any{"message": map[string]any{"role": "assistant", "content": "hello"}, "done": false},
			map[string]any{"message": map[string]any{"role": "assistant", "content": " world"}, "done": false},
			map[string]any{"message": map[string]any{"role": "assistant", "content": ""}, "done": true, "done_reason": "stop",
				"prompt_eval_count": 10, "eval_count": 2},
		)
	}))
	defer srv.Close()

	model := toModel("m:latest", srv.URL, 8192, []string{"completion", "tools"})
	p := New(Options{URL: srv.URL})
	user := msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}
	events, wait := p.Stream(context.Background(), model, []msg.Message{user}, provider.StreamOptions{})
	var sawTextStart, sawTextEnd bool
	for ev := range events {
		switch ev.Type {
		case msg.EventTextStart:
			sawTextStart = true
		case msg.EventTextEnd:
			sawTextEnd = true
			if ev.Content != "hello world" {
				t.Fatalf("text_end content = %q, want %q", ev.Content, "hello world")
			}
		}
	}
	final, err := wait()
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if !sawTextStart || !sawTextEnd {
		t.Fatalf("sawTextStart=%v sawTextEnd=%v, want both true", sawTextStart, sawTextEnd)
	}
	if final.StopReason != msg.StopStop {
		t.Fatalf("StopReason = %q, want stop", final.StopReason)
	}
	if final.Usage.Input != 10 || final.Usage.Output != 2 || final.Usage.TotalTokens != 12 {
		t.Fatalf("Usage = %+v, want Input=10 Output=2 TotalTokens=12", final.Usage)
	}
}

// --- parity: tool calls ---

func TestNativeToolCallRoundTrip(t *testing.T) {
	var secondReq nativeChatRequest
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		req := readNDJSONRequest(t, r)
		if call == 1 {
			writeNDJSON(w, map[string]any{
				"message": map[string]any{
					"role": "assistant", "content": "",
					"tool_calls": []map[string]any{{
						"id":       "call_1",
						"function": map[string]any{"name": "get_weather", "arguments": map[string]any{"city": "Boston"}},
					}},
				},
				"done": true, "done_reason": "stop",
				"prompt_eval_count": 20, "eval_count": 5,
			})
			return
		}
		secondReq = req
		writeNDJSON(w, map[string]any{
			"message": map[string]any{"role": "assistant", "content": "72F and sunny."},
			"done":    true, "done_reason": "stop",
			"prompt_eval_count": 30, "eval_count": 8,
		})
	}))
	defer srv.Close()

	model := toModel("m:latest", srv.URL, 8192, []string{"completion", "tools"})
	p := New(Options{URL: srv.URL})
	user := msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("weather in Boston?")}}
	tools := []provider.ToolDef{{Name: "get_weather", Description: "Get weather", Parameters: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`)}}

	events, wait := p.Stream(context.Background(), model, []msg.Message{user}, provider.StreamOptions{Tools: tools})
	var gotCall *msg.ToolCall
	for ev := range events {
		if ev.Type == msg.EventToolCallEnd {
			gotCall = ev.ToolCall
		}
	}
	final, err := wait()
	if err != nil {
		t.Fatalf("stream 1: %v", err)
	}
	if final.StopReason != msg.StopToolUse {
		t.Fatalf("StopReason = %q, want toolUse (done_reason was \"stop\" but a tool call was present)", final.StopReason)
	}
	if gotCall == nil || gotCall.Name != "get_weather" || gotCall.Arguments["city"] != "Boston" {
		t.Fatalf("tool call = %+v, want get_weather(city=Boston)", gotCall)
	}

	// Round-trip: replay the assistant tool call plus its result, confirm
	// the request carries role:"tool" with a plain string result.
	assistantMsg := msg.AssistantMessage{Role: msg.RoleAssistant, Content: msg.Blocks{*gotCall}}
	toolResult := msg.ToolResultMessage{Role: msg.RoleToolResult, Content: msg.Blocks{msg.Text("72F and sunny")}, ToolCallID: gotCall.ID, ToolName: "get_weather"}
	events2, wait2 := p.Stream(context.Background(), model, []msg.Message{user, assistantMsg, toolResult}, provider.StreamOptions{Tools: tools})
	for range events2 {
	}
	if _, err := wait2(); err != nil {
		t.Fatalf("stream 2: %v", err)
	}

	if len(secondReq.Messages) != 3 {
		t.Fatalf("second request has %d messages, want 3", len(secondReq.Messages))
	}
	toolMsg := secondReq.Messages[2]
	if toolMsg.Role != "tool" || toolMsg.Content != "72F and sunny" || toolMsg.ToolName != "get_weather" {
		t.Fatalf("tool result message = %+v, want role=tool content=%q tool_name=get_weather", toolMsg, "72F and sunny")
	}
	assistantWire := secondReq.Messages[1]
	if len(assistantWire.ToolCalls) != 1 || assistantWire.ToolCalls[0].ID != gotCall.ID || assistantWire.ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("replayed assistant tool call = %+v, want id=%s name=get_weather", assistantWire.ToolCalls, gotCall.ID)
	}
}

// --- parity: thinking ---

func TestNativeThinkingStream(t *testing.T) {
	var gotThink *bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readNDJSONRequest(t, r)
		gotThink = req.Think
		writeNDJSON(w,
			map[string]any{"message": map[string]any{"role": "assistant", "content": "", "thinking": "let me "}, "done": false},
			map[string]any{"message": map[string]any{"role": "assistant", "content": "", "thinking": "think"}, "done": false},
			map[string]any{"message": map[string]any{"role": "assistant", "content": "4"}, "done": true, "done_reason": "stop",
				"prompt_eval_count": 5, "eval_count": 5},
		)
	}))
	defer srv.Close()

	model := toModel("qwen3-cc:latest", srv.URL, 32768, []string{"completion", "tools", "thinking"})
	p := New(Options{URL: srv.URL})
	user := msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("2+2?")}}
	events, wait := p.Stream(context.Background(), model, []msg.Message{user}, provider.StreamOptions{ThinkingLevel: provider.ThinkingMedium})
	var thinkingText string
	for ev := range events {
		if ev.Type == msg.EventThinkingEnd {
			thinkingText = ev.Content
		}
	}
	if _, err := wait(); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if gotThink == nil || !*gotThink {
		t.Fatalf("request think = %v, want true (ThinkingMedium requested)", gotThink)
	}
	if thinkingText != "let me think" {
		t.Fatalf("thinking text = %q, want %q", thinkingText, "let me think")
	}
}

// TestNativeThinkingOffSendsExplicitFalse covers suppression parity: a
// reasoning-capable model with no thinking requested must still get an
// explicit think:false (never an omitted field), matching the model's
// ThinkingLevelMap mapping ThinkingOff -> "off" and the comment in
// ollama.go's thinkingLevelMap about models that ignore an absent switch.
func TestNativeThinkingOffSendsExplicitFalse(t *testing.T) {
	var gotThink *bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readNDJSONRequest(t, r)
		gotThink = req.Think
		writeNDJSON(w, map[string]any{"message": map[string]any{"role": "assistant", "content": "ok"}, "done": true, "done_reason": "stop"})
	}))
	defer srv.Close()

	model := toModel("qwen3-cc:latest", srv.URL, 32768, []string{"completion", "tools", "thinking"})
	p := New(Options{URL: srv.URL})
	user := msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}
	events, wait := p.Stream(context.Background(), model, []msg.Message{user}, provider.StreamOptions{})
	for range events {
	}
	if _, err := wait(); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if gotThink == nil || *gotThink {
		t.Fatalf("request think = %v, want explicit false", gotThink)
	}
}

// --- parity: images ---

func TestNativeImageContent(t *testing.T) {
	var gotImages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readNDJSONRequest(t, r)
		for _, m := range req.Messages {
			if m.Role == "user" {
				gotImages = m.Images
			}
		}
		writeNDJSON(w, map[string]any{"message": map[string]any{"role": "assistant", "content": "I see a cat"}, "done": true, "done_reason": "stop"})
	}))
	defer srv.Close()

	model := toModel("vision:latest", srv.URL, 8192, []string{"completion", "tools", "vision"})
	p := New(Options{URL: srv.URL})
	user := msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("what is this?"), msg.Image("image/png", "QkFTRTY0")}}
	events, wait := p.Stream(context.Background(), model, []msg.Message{user}, provider.StreamOptions{})
	for range events {
	}
	if _, err := wait(); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(gotImages) != 1 || gotImages[0] != "QkFTRTY0" {
		t.Fatalf("images = %v, want bare base64 [QkFTRTY0] (no data: URI wrapper)", gotImages)
	}
}

// --- errors ---

// TestNativeMidStreamErrorLine covers a trailing NDJSON {"error":...} line
// (e.g. the model crashed mid-generation): the stream must end in error,
// not be silently swallowed as a clean "done" with no done:true ever seen.
func TestNativeMidStreamErrorLine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fl := w.(http.Flusher)
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":"partial"},"done":false}`)
		fl.Flush()
		fmt.Fprintln(w, `{"error":"model crashed: out of memory"}`)
		fl.Flush()
	}))
	defer srv.Close()

	model := toModel("m:latest", srv.URL, 8192, []string{"completion", "tools"})
	p := New(Options{URL: srv.URL})
	user := msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}
	events, wait := p.Stream(context.Background(), model, []msg.Message{user}, provider.StreamOptions{})
	var sawErrorEvent bool
	for ev := range events {
		if ev.Type == msg.EventError {
			sawErrorEvent = true
		}
	}
	_, err := wait()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "out of memory") {
		t.Fatalf("error = %q, want it to mention the server's message", err.Error())
	}
	if !sawErrorEvent {
		t.Fatal("expected an EventError on the stream")
	}
}

// TestNativeHTTPError covers a plain HTTP-level failure (e.g. unknown
// model): Ollama returns a non-2xx status with a {"error":...} JSON body
// before any NDJSON streaming begins.
func TestNativeHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"model 'nope' not found"}`))
	}))
	defer srv.Close()

	model := toModel("nope:latest", srv.URL, 8192, []string{"completion", "tools"})
	p := New(Options{URL: srv.URL})
	user := msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}
	events, wait := p.Stream(context.Background(), model, []msg.Message{user}, provider.StreamOptions{})
	for range events {
	}
	_, err := wait()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "Not Found") && !strings.Contains(err.Error(), "404") {
		t.Fatalf("error = %q, want it to mention the HTTP status", err.Error())
	}
}

// TestNativeCancellation aborts mid-stream and expects a clean "aborted"
// outcome, not a hang or a misreported error that would be retried.
func TestNativeCancellation(t *testing.T) {
	started := make(chan struct{})
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fl := w.(http.Flusher)
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":"partial"},"done":false}`)
		fl.Flush()
		close(started)
		<-block // held open until the test cancels the request context
	}))
	defer srv.Close()
	defer close(block)

	model := toModel("m:latest", srv.URL, 8192, []string{"completion", "tools"})
	p := New(Options{URL: srv.URL})
	user := msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}

	ctx, cancel := context.WithCancel(context.Background())
	events, wait := p.Stream(ctx, model, []msg.Message{user}, provider.StreamOptions{})
	<-started
	cancel()
	for range events {
	}
	final, err := wait()
	if err == nil {
		t.Fatal("expected an error after cancellation, got nil")
	}
	if final != nil {
		t.Fatalf("expected a nil final message on an aborted stream, got %+v", final)
	}
}

// A slow server, whose headers come only after it has read the prompt and
// whose tokens trickle in, streams to the end through the default client.
func TestStreamSlowServerCompletes(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		time.Sleep(300 * time.Millisecond) // reading the prompt
		w.Header().Set("Content-Type", "application/x-ndjson")
		fl := w.(http.Flusher)
		for i := 0; i < 5; i++ {
			fmt.Fprintf(w, `{"message":{"role":"assistant","content":"t%d "},"done":false}`+"\n", i)
			fl.Flush()
			time.Sleep(100 * time.Millisecond)
		}
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`)
		fl.Flush()
	}))
	defer srv.Close()

	p := New(Options{URL: srv.URL})
	model := toModel("slow:latest", srv.URL, 8192, []string{"completion", "tools"})
	user := msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}
	events, wait := p.Stream(context.Background(), model, []msg.Message{user}, provider.StreamOptions{})
	for range events {
	}
	final, err := wait()
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	text := ""
	for _, b := range final.Content {
		if tc, ok := b.(msg.TextContent); ok {
			text += tc.Text
		}
	}
	if text != "t0 t1 t2 t3 t4 " {
		t.Fatalf("text = %q", text)
	}
	if atomic.LoadInt32(&requests) != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}
