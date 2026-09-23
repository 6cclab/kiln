package faux

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func startTestServer(t *testing.T, yamlDoc string) (*Server, string) {
	t.Helper()
	s, err := New(Options{ScriptYAML: yamlDoc})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	addr, err := s.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, "http://" + addr
}

// --- a small hand-written SSE reader used to exercise the streaming
// parsers exactly like a real client would.

type sseEvent struct {
	event string
	data  string
}

func readSSE(t *testing.T, body io.Reader) []sseEvent {
	t.Helper()
	var events []sseEvent
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var curEvent string
	var dataLines []string
	flush := func() {
		if curEvent == "" && len(dataLines) == 0 {
			return
		}
		events = append(events, sseEvent{event: curEvent, data: strings.Join(dataLines, "\n")})
		curEvent = ""
		dataLines = nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		switch {
		case strings.HasPrefix(line, "event:"):
			curEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	flush()
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan sse: %v", err)
	}
	return events
}

func postJSON(t *testing.T, url string, body map[string]any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	return resp
}

// --- Anthropic streaming ----------------------------------------------

func TestAnthropicStreamingTextThinkingToolUse(t *testing.T) {
	yamlDoc := `
model: faux-1
steps:
  - text: "Hello there"
  - thinking: "pondering"
  - tool_call: {name: read, args: {path: src/math.js}, id: tc1}
`
	_, base := startTestServer(t, yamlDoc)

	resp := postJSON(t, base+"/v1/messages", map[string]any{
		"model":  "faux-1",
		"stream": true,
		"messages": []map[string]any{
			{"role": "user", "content": "hi"},
		},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	events := readSSE(t, resp.Body)

	var text, thinking string
	var toolInputJSON string
	var toolName, toolID string
	var stopReason string
	var usageInput, usageOutput int
	var sawMessageStart, sawMessageStop bool

	for _, ev := range events {
		var payload map[string]any
		if err := json.Unmarshal([]byte(ev.data), &payload); err != nil {
			t.Fatalf("bad json in event %q: %v (%s)", ev.event, err, ev.data)
		}
		switch payload["type"] {
		case "message_start":
			sawMessageStart = true
			msg := payload["message"].(map[string]any)
			usage := msg["usage"].(map[string]any)
			usageInput = int(usage["input_tokens"].(float64))
		case "content_block_start":
			cb := payload["content_block"].(map[string]any)
			if cb["type"] == "tool_use" {
				toolName, _ = cb["name"].(string)
				toolID, _ = cb["id"].(string)
			}
		case "content_block_delta":
			delta := payload["delta"].(map[string]any)
			switch delta["type"] {
			case "text_delta":
				text += delta["text"].(string)
			case "thinking_delta":
				thinking += delta["thinking"].(string)
			case "input_json_delta":
				toolInputJSON += delta["partial_json"].(string)
			}
		case "message_delta":
			delta := payload["delta"].(map[string]any)
			stopReason, _ = delta["stop_reason"].(string)
			usage := payload["usage"].(map[string]any)
			usageOutput = int(usage["output_tokens"].(float64))
		case "message_stop":
			sawMessageStop = true
		}
	}

	if !sawMessageStart || !sawMessageStop {
		t.Fatalf("missing message_start/message_stop: start=%v stop=%v", sawMessageStart, sawMessageStop)
	}
	if text != "Hello there" {
		t.Errorf("text = %q, want %q", text, "Hello there")
	}
	if thinking != "pondering" {
		t.Errorf("thinking = %q, want %q", thinking, "pondering")
	}
	if toolName != "read" || toolID != "toolu_tc1" {
		t.Errorf("tool = %q/%q, want read/toolu_tc1", toolName, toolID)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(toolInputJSON), &args); err != nil {
		t.Fatalf("tool args not valid json: %v (%s)", err, toolInputJSON)
	}
	if args["path"] != "src/math.js" {
		t.Errorf("tool args path = %v", args["path"])
	}
	if stopReason != "tool_use" {
		t.Errorf("stop_reason = %q, want tool_use", stopReason)
	}
	if usageInput != 100 || usageOutput != 50 {
		t.Errorf("usage = %d/%d, want default 100/50", usageInput, usageOutput)
	}
}

// --- OpenAI streaming ----------------------------------------------------

func TestOpenAIStreamingTextThinkingToolCalls(t *testing.T) {
	yamlDoc := `
model: faux-1
steps:
  - text: "Hello there"
  - thinking: "pondering"
  - tool_call: {name: read, args: {path: src/math.js}, id: tc1}
`
	_, base := startTestServer(t, yamlDoc)

	resp := postJSON(t, base+"/v1/chat/completions", map[string]any{
		"model":          "faux-1",
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
		"messages": []map[string]any{
			{"role": "user", "content": "hi"},
		},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var text, thinking, toolArgs, toolName, toolID, finishReason string
	var usagePrompt, usageCompletion int
	var sawDone bool

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			sawDone = true
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("bad chunk json: %v (%s)", err, data)
		}
		choices := chunk["choices"].([]any)
		choice := choices[0].(map[string]any)
		if delta, ok := choice["delta"].(map[string]any); ok {
			if c, ok := delta["content"].(string); ok {
				text += c
			}
			if r, ok := delta["reasoning_content"].(string); ok {
				thinking += r
			}
			if tcs, ok := delta["tool_calls"].([]any); ok {
				for _, tcRaw := range tcs {
					tc := tcRaw.(map[string]any)
					if id, ok := tc["id"].(string); ok {
						toolID = id
					}
					if fn, ok := tc["function"].(map[string]any); ok {
						if n, ok := fn["name"].(string); ok {
							toolName = n
						}
						if a, ok := fn["arguments"].(string); ok {
							toolArgs += a
						}
					}
				}
			}
		}
		if fr, ok := choice["finish_reason"].(string); ok {
			finishReason = fr
		}
		if usage, ok := chunk["usage"].(map[string]any); ok {
			usagePrompt = int(usage["prompt_tokens"].(float64))
			usageCompletion = int(usage["completion_tokens"].(float64))
		}
	}

	if !sawDone {
		t.Fatal("did not see [DONE]")
	}
	if text != "Hello there" {
		t.Errorf("text = %q", text)
	}
	if thinking != "pondering" {
		t.Errorf("thinking = %q", thinking)
	}
	if toolName != "read" || toolID != "call_tc1" {
		t.Errorf("tool = %q/%q", toolName, toolID)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(toolArgs), &args); err != nil {
		t.Fatalf("tool args not valid json: %v (%s)", err, toolArgs)
	}
	if args["path"] != "src/math.js" {
		t.Errorf("tool args path = %v", args["path"])
	}
	if finishReason != "tool_calls" {
		t.Errorf("finish_reason = %q", finishReason)
	}
	if usagePrompt != 100 || usageCompletion != 50 {
		t.Errorf("usage = %d/%d", usagePrompt, usageCompletion)
	}
}

// --- non-streaming ---------------------------------------------------------

func TestAnthropicNonStreaming(t *testing.T) {
	_, base := startTestServer(t, `
model: faux-1
steps:
  - text: "hi there"
    usage: {input: 7, output: 3}
`)
	resp := postJSON(t, base+"/v1/messages", map[string]any{
		"model": "faux-1",
		"messages": []map[string]any{
			{"role": "user", "content": "hi"},
		},
	})
	defer resp.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	content := payload["content"].([]any)
	block := content[0].(map[string]any)
	if block["text"] != "hi there" {
		t.Errorf("text = %v", block["text"])
	}
	if payload["stop_reason"] != "end_turn" {
		t.Errorf("stop_reason = %v", payload["stop_reason"])
	}
	usage := payload["usage"].(map[string]any)
	if int(usage["input_tokens"].(float64)) != 7 || int(usage["output_tokens"].(float64)) != 3 {
		t.Errorf("usage = %v", usage)
	}
}

func TestOpenAINonStreaming(t *testing.T) {
	_, base := startTestServer(t, `
model: faux-1
steps:
  - text: "hi there"
    usage: {input: 7, output: 3}
`)
	resp := postJSON(t, base+"/v1/chat/completions", map[string]any{
		"model": "faux-1",
		"messages": []map[string]any{
			{"role": "user", "content": "hi"},
		},
	})
	defer resp.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	choices := payload["choices"].([]any)
	choice := choices[0].(map[string]any)
	message := choice["message"].(map[string]any)
	if message["content"] != "hi there" {
		t.Errorf("content = %v", message["content"])
	}
	if choice["finish_reason"] != "stop" {
		t.Errorf("finish_reason = %v", choice["finish_reason"])
	}
	usage := payload["usage"].(map[string]any)
	if int(usage["prompt_tokens"].(float64)) != 7 || int(usage["completion_tokens"].(float64)) != 3 {
		t.Errorf("usage = %v", usage)
	}
}

// --- on_tool_result matching and mismatch recording -----------------------

func TestOnToolResultMatchAndMismatch(t *testing.T) {
	s, base := startTestServer(t, `
model: faux-1
steps:
  - tool_call: {name: read, args: {path: src/math.js}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "Fixed."
`)
	// First request: triggers the tool_call turn.
	resp1 := postJSON(t, base+"/v1/messages", map[string]any{
		"model":    "faux-1",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	resp1.Body.Close()

	// Second request: does NOT contain a tool_result for tc1 -> mismatch recorded.
	resp2 := postJSON(t, base+"/v1/messages", map[string]any{
		"model":    "faux-1",
		"messages": []map[string]any{{"role": "user", "content": "no tool result here"}},
	})
	var payload2 map[string]any
	json.NewDecoder(resp2.Body).Decode(&payload2)
	resp2.Body.Close()
	content2 := payload2["content"].([]any)
	if content2[0].(map[string]any)["text"] != "Fixed." {
		t.Errorf("expected turn to proceed anyway, got %v", payload2)
	}

	st := s.engine.state()
	if len(st.Errors) == 0 {
		t.Fatal("expected a recorded mismatch, got none")
	}
	if !strings.Contains(st.Errors[0], "tc1") {
		t.Errorf("mismatch message = %q, want mention of tc1", st.Errors[0])
	}
}

func TestOnToolResultMatchSucceeds(t *testing.T) {
	s, base := startTestServer(t, `
model: faux-1
steps:
  - tool_call: {name: read, args: {path: src/math.js}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "Fixed."
`)
	resp1 := postJSON(t, base+"/v1/messages", map[string]any{
		"model":    "faux-1",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	resp1.Body.Close()

	resp2 := postJSON(t, base+"/v1/messages", map[string]any{
		"model": "faux-1",
		"messages": []map[string]any{
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": []map[string]any{{"type": "tool_use", "id": "toolu_tc1", "name": "read", "input": map[string]any{}}}},
			{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "toolu_tc1", "content": "file contents"}}},
		},
	})
	resp2.Body.Close()

	st := s.engine.state()
	if len(st.Errors) != 0 {
		t.Fatalf("expected no mismatch, got %v", st.Errors)
	}
}

// --- exhaustion -------------------------------------------------------------

func TestExhaustion(t *testing.T) {
	s, base := startTestServer(t, `
model: faux-1
steps:
  - text: "only reply"
`)
	resp1 := postJSON(t, base+"/v1/messages", map[string]any{
		"model": "faux-1", "messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	resp1.Body.Close()

	resp2 := postJSON(t, base+"/v1/messages", map[string]any{
		"model": "faux-1", "messages": []map[string]any{{"role": "user", "content": "hi again"}},
	})
	var payload map[string]any
	json.NewDecoder(resp2.Body).Decode(&payload)
	resp2.Body.Close()
	content := payload["content"].([]any)
	if content[0].(map[string]any)["text"] != exhaustedText {
		t.Errorf("expected exhausted text, got %v", payload)
	}

	st := s.engine.state()
	if !st.Exhausted {
		t.Error("expected exhausted = true")
	}
}

// --- error step --------------------------------------------------------------

func TestErrorStepFiresOnce(t *testing.T) {
	_, base := startTestServer(t, `
model: faux-1
steps:
  - error: {status: 529, type: overloaded_error, message: "Overloaded"}
  - text: "recovered"
`)
	resp1 := postJSON(t, base+"/v1/messages", map[string]any{
		"model": "faux-1", "messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if resp1.StatusCode != 529 {
		t.Fatalf("status = %d, want 529", resp1.StatusCode)
	}
	var errPayload map[string]any
	json.NewDecoder(resp1.Body).Decode(&errPayload)
	resp1.Body.Close()
	errObj := errPayload["error"].(map[string]any)
	if errObj["type"] != "overloaded_error" {
		t.Errorf("error type = %v", errObj["type"])
	}

	resp2 := postJSON(t, base+"/v1/messages", map[string]any{
		"model": "faux-1", "messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second request status = %d, want 200", resp2.StatusCode)
	}
	var payload map[string]any
	json.NewDecoder(resp2.Body).Decode(&payload)
	resp2.Body.Close()
	content := payload["content"].([]any)
	if content[0].(map[string]any)["text"] != "recovered" {
		t.Errorf("expected recovered text, got %v", payload)
	}
}

// --- hot-swap ------------------------------------------------------------

func TestHotSwapScript(t *testing.T) {
	_, base := startTestServer(t, `
model: faux-1
steps:
  - text: "original"
`)
	resp1 := postJSON(t, base+"/v1/messages", map[string]any{
		"model": "faux-1", "messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	var p1 map[string]any
	json.NewDecoder(resp1.Body).Decode(&p1)
	resp1.Body.Close()
	if p1["content"].([]any)[0].(map[string]any)["text"] != "original" {
		t.Fatalf("expected original reply, got %v", p1)
	}

	swapResp, err := http.Post(base+"/_faux/script", "application/x-yaml", strings.NewReader(`
model: faux-2
steps:
  - text: "swapped"
`))
	if err != nil {
		t.Fatalf("swap: %v", err)
	}
	swapResp.Body.Close()

	resp2 := postJSON(t, base+"/v1/messages", map[string]any{
		"model": "faux-2", "messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	var p2 map[string]any
	json.NewDecoder(resp2.Body).Decode(&p2)
	resp2.Body.Close()
	if p2["content"].([]any)[0].(map[string]any)["text"] != "swapped" {
		t.Fatalf("expected swapped reply, got %v", p2)
	}
}

// --- request recording ----------------------------------------------------

func TestRequestRecordingIncludesSystemToolsMessages(t *testing.T) {
	s, base := startTestServer(t, `
model: faux-1
steps:
  - text: "ok"
`)
	resp := postJSON(t, base+"/v1/messages", map[string]any{
		"model":  "faux-1",
		"system": "You are a helpful test harness.",
		"tools": []map[string]any{
			{"name": "read", "description": "reads a file", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}},
		},
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	resp.Body.Close()

	reqs := s.Requests()
	if len(reqs) != 1 {
		t.Fatalf("Requests() len = %d, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Protocol != "anthropic" {
		t.Errorf("protocol = %q", r.Protocol)
	}
	if r.System != "You are a helpful test harness." {
		t.Errorf("system = %q", r.System)
	}
	if len(r.Tools) != 1 || r.Tools[0].Name != "read" {
		t.Errorf("tools = %+v", r.Tools)
	}
	var msgs []map[string]any
	if err := json.Unmarshal(r.Messages, &msgs); err != nil {
		t.Fatalf("unmarshal recorded messages: %v", err)
	}
	if len(msgs) != 1 || msgs[0]["role"] != "user" {
		t.Errorf("messages = %v", msgs)
	}
	if len(r.Body) == 0 {
		t.Error("raw body not recorded")
	}
}

// --- delay ------------------------------------------------------------------

func TestDelayStepSleeps(t *testing.T) {
	_, base := startTestServer(t, `
model: faux-1
steps:
  - delay: 30ms
  - text: "after delay"
`)
	start := time.Now()
	resp := postJSON(t, base+"/v1/messages", map[string]any{
		"model": "faux-1", "messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	elapsed := time.Since(start)
	resp.Body.Close()
	if elapsed < 25*time.Millisecond {
		t.Errorf("elapsed = %v, want >= ~30ms", elapsed)
	}
}
