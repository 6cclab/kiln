package faux

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
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

	st := s.engine.forModel("faux-1").state()
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

	st := s.engine.forModel("faux-1").state()
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

	st := s.engine.forModel("faux-1").state()
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

// --- multi-model scripts -----------------------------------------------

func TestMultiModelIndependentScripts(t *testing.T) {
	// Each model gets a two-turn script (a tool_call, then a gated
	// on_tool_result reply), so driving it end to end requires two
	// requests per model and exercises that model's cursor advancing
	// independently of the other's.
	s, base := startTestServer(t, `
models:
  faux-1:
    - tool_call: {name: read, args: {path: one.js}, id: t1}
    - on_tool_result: t1
      then:
        - text: "one-done"
  faux-2:
    - tool_call: {name: read, args: {path: two.js}, id: t2}
    - on_tool_result: t2
      then:
        - text: "two-done"
`)

	// driveModel runs a model's full two-turn conversation (tool_call,
	// then the tool_result-gated reply) and returns the final text.
	driveModel := func(model, toolID string) string {
		t.Helper()
		resp1 := postJSON(t, base+"/v1/messages", map[string]any{
			"model":    model,
			"messages": []map[string]any{{"role": "user", "content": "hi"}},
		})
		var p1 map[string]any
		json.NewDecoder(resp1.Body).Decode(&p1)
		resp1.Body.Close()
		content1 := p1["content"].([]any)
		block := content1[0].(map[string]any)
		if block["type"] != "tool_use" || block["name"] != "read" {
			t.Fatalf("%s turn 1 = %v, want a read tool_use", model, p1)
		}

		resp2 := postJSON(t, base+"/v1/messages", map[string]any{
			"model": model,
			"messages": []map[string]any{
				{"role": "user", "content": "hi"},
				{"role": "assistant", "content": []map[string]any{{"type": "tool_use", "id": "toolu_" + toolID, "name": "read", "input": map[string]any{}}}},
				{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "toolu_" + toolID, "content": "ok"}}},
			},
		})
		var p2 map[string]any
		json.NewDecoder(resp2.Body).Decode(&p2)
		resp2.Body.Close()
		content2 := p2["content"].([]any)
		return content2[0].(map[string]any)["text"].(string)
	}

	var wg sync.WaitGroup
	var got1, got2 string
	wg.Add(2)
	go func() {
		defer wg.Done()
		got1 = driveModel("faux-1", "t1")
	}()
	go func() {
		defer wg.Done()
		got2 = driveModel("faux-2", "t2")
	}()
	wg.Wait()

	if got1 != "one-done" {
		t.Errorf("faux-1 final text = %q, want one-done", got1)
	}
	if got2 != "two-done" {
		t.Errorf("faux-2 final text = %q, want two-done", got2)
	}

	st1 := s.engine.forModel("faux-1").state()
	st2 := s.engine.forModel("faux-2").state()
	if st1.StepIndex != 2 {
		t.Errorf("faux-1 stepIndex = %d, want 2 (both its turns consumed)", st1.StepIndex)
	}
	if st2.StepIndex != 2 {
		t.Errorf("faux-2 stepIndex = %d, want 2 (both its turns consumed)", st2.StepIndex)
	}
	if len(st1.Errors) != 0 {
		t.Errorf("faux-1 unexpected mismatches: %v", st1.Errors)
	}
	if len(st2.Errors) != 0 {
		t.Errorf("faux-2 unexpected mismatches: %v", st2.Errors)
	}
}

func TestUnknownModelReturns400(t *testing.T) {
	_, base := startTestServer(t, `
models:
  faux-1:
    - text: "hi"
  faux-2:
    - text: "hi"
`)
	resp := postJSON(t, base+"/v1/messages", map[string]any{
		"model":    "faux-nope",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	errObj, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("payload = %v, missing error object", payload)
	}
	msg, _ := errObj["message"].(string)
	if !strings.Contains(msg, "faux-nope") || !strings.Contains(msg, "faux-1") || !strings.Contains(msg, "faux-2") {
		t.Errorf("message = %q, want mention of faux-nope, faux-1, faux-2", msg)
	}

	respOA := postJSON(t, base+"/v1/chat/completions", map[string]any{
		"model":    "faux-nope",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	defer respOA.Body.Close()
	if respOA.StatusCode != http.StatusBadRequest {
		t.Fatalf("openai status = %d, want 400", respOA.StatusCode)
	}
}

func TestSingleScriptFormIsOneEntryModelsMap(t *testing.T) {
	s, base := startTestServer(t, `
model: faux-1
steps:
  - text: "hi there"
    usage: {input: 7, output: 3}
`)
	resp := postJSON(t, base+"/v1/messages", map[string]any{
		"model":    "faux-1",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	content := payload["content"].([]any)
	if content[0].(map[string]any)["text"] != "hi there" {
		t.Errorf("text = %v", content[0])
	}

	names := s.engine.modelNames()
	if len(names) != 1 || names[0] != "faux-1" {
		t.Errorf("modelNames() = %v, want [faux-1]", names)
	}
}

// --- tool_calls (multiple tool calls in one turn) -----------------------

func TestAnthropicToolCallsNonStreaming(t *testing.T) {
	_, base := startTestServer(t, `
model: faux-1
steps:
  - tool_calls:
      - {name: read, args: {path: a.js}, id: tc1}
      - {name: read, args: {path: b.js}, id: tc2}
`)
	resp := postJSON(t, base+"/v1/messages", map[string]any{
		"model":    "faux-1",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	content := payload["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content len = %d, want 2: %v", len(content), content)
	}
	b0 := content[0].(map[string]any)
	b1 := content[1].(map[string]any)
	if b0["type"] != "tool_use" || b0["id"] != "toolu_tc1" || b0["name"] != "read" {
		t.Errorf("block0 = %v", b0)
	}
	if b1["type"] != "tool_use" || b1["id"] != "toolu_tc2" || b1["name"] != "read" {
		t.Errorf("block1 = %v", b1)
	}
	if payload["stop_reason"] != "tool_use" {
		t.Errorf("stop_reason = %v", payload["stop_reason"])
	}
}

func TestAnthropicToolCallsStreaming(t *testing.T) {
	_, base := startTestServer(t, `
model: faux-1
steps:
  - tool_calls:
      - {name: read, args: {path: a.js}, id: tc1}
      - {name: read, args: {path: b.js}, id: tc2}
`)
	resp := postJSON(t, base+"/v1/messages", map[string]any{
		"model":  "faux-1",
		"stream": true,
		"messages": []map[string]any{
			{"role": "user", "content": "hi"},
		},
	})
	defer resp.Body.Close()
	events := readSSE(t, resp.Body)

	var toolUseIDs []string
	var toolUseIndexes []int
	for _, ev := range events {
		var payload map[string]any
		if err := json.Unmarshal([]byte(ev.data), &payload); err != nil {
			t.Fatalf("bad json in event %q: %v (%s)", ev.event, err, ev.data)
		}
		if payload["type"] != "content_block_start" {
			continue
		}
		cb := payload["content_block"].(map[string]any)
		if cb["type"] != "tool_use" {
			continue
		}
		toolUseIDs = append(toolUseIDs, cb["id"].(string))
		toolUseIndexes = append(toolUseIndexes, int(payload["index"].(float64)))
	}
	if len(toolUseIDs) != 2 {
		t.Fatalf("saw %d tool_use content_block_start events, want 2: %v", len(toolUseIDs), toolUseIDs)
	}
	if toolUseIDs[0] != "toolu_tc1" || toolUseIDs[1] != "toolu_tc2" {
		t.Errorf("tool use ids = %v, want [toolu_tc1 toolu_tc2]", toolUseIDs)
	}
	if toolUseIndexes[0] != 0 || toolUseIndexes[1] != 1 {
		t.Errorf("tool use block indexes = %v, want [0 1]", toolUseIndexes)
	}
}

func TestOpenAIToolCallsNonStreaming(t *testing.T) {
	_, base := startTestServer(t, `
model: faux-1
steps:
  - tool_calls:
      - {name: read, args: {path: a.js}, id: tc1}
      - {name: read, args: {path: b.js}, id: tc2}
`)
	resp := postJSON(t, base+"/v1/chat/completions", map[string]any{
		"model":    "faux-1",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	choices := payload["choices"].([]any)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	toolCalls := message["tool_calls"].([]any)
	if len(toolCalls) != 2 {
		t.Fatalf("tool_calls len = %d, want 2: %v", len(toolCalls), toolCalls)
	}
	tc0 := toolCalls[0].(map[string]any)
	tc1 := toolCalls[1].(map[string]any)
	if tc0["id"] != "call_tc1" || tc1["id"] != "call_tc2" {
		t.Errorf("tool call ids = %v / %v", tc0["id"], tc1["id"])
	}
}

func TestOpenAIToolCallsStreaming(t *testing.T) {
	_, base := startTestServer(t, `
model: faux-1
steps:
  - tool_calls:
      - {name: read, args: {path: a.js}, id: tc1}
      - {name: read, args: {path: b.js}, id: tc2}
`)
	resp := postJSON(t, base+"/v1/chat/completions", map[string]any{
		"model":  "faux-1",
		"stream": true,
		"messages": []map[string]any{
			{"role": "user", "content": "hi"},
		},
	})
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	seenIDs := map[string]bool{}
	seenIndexes := map[int]bool{}
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("bad chunk json: %v (%s)", err, data)
		}
		choice := chunk["choices"].([]any)[0].(map[string]any)
		delta, ok := choice["delta"].(map[string]any)
		if !ok {
			continue
		}
		tcs, ok := delta["tool_calls"].([]any)
		if !ok {
			continue
		}
		for _, tcRaw := range tcs {
			tc := tcRaw.(map[string]any)
			seenIndexes[int(tc["index"].(float64))] = true
			if id, ok := tc["id"].(string); ok {
				seenIDs[id] = true
			}
		}
	}
	if !seenIDs["call_tc1"] || !seenIDs["call_tc2"] {
		t.Errorf("seenIDs = %v, want call_tc1 and call_tc2", seenIDs)
	}
	if !seenIndexes[0] || !seenIndexes[1] {
		t.Errorf("seenIndexes = %v, want 0 and 1", seenIndexes)
	}
}

// --- on_tool_results (multi-id gate) -------------------------------------

func TestOnToolResultsGateBothPresent(t *testing.T) {
	s, base := startTestServer(t, `
model: faux-1
steps:
  - tool_calls:
      - {name: read, args: {path: a.js}, id: tc1}
      - {name: read, args: {path: b.js}, id: tc2}
  - on_tool_results: [tc1, tc2]
    then:
      - text: "both done"
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
			{"role": "assistant", "content": []map[string]any{
				{"type": "tool_use", "id": "toolu_tc1", "name": "read", "input": map[string]any{}},
				{"type": "tool_use", "id": "toolu_tc2", "name": "read", "input": map[string]any{}},
			}},
			{"role": "user", "content": []map[string]any{
				{"type": "tool_result", "tool_use_id": "toolu_tc1", "content": "a"},
				{"type": "tool_result", "tool_use_id": "toolu_tc2", "content": "b"},
			}},
		},
	})
	var payload map[string]any
	json.NewDecoder(resp2.Body).Decode(&payload)
	resp2.Body.Close()
	content := payload["content"].([]any)
	if content[0].(map[string]any)["text"] != "both done" {
		t.Errorf("expected 'both done', got %v", payload)
	}

	st := s.engine.forModel("faux-1").state()
	if len(st.Errors) != 0 {
		t.Fatalf("expected no mismatch when both results present, got %v", st.Errors)
	}
}

func TestOnToolResultsGateOneMissing(t *testing.T) {
	s, base := startTestServer(t, `
model: faux-1
steps:
  - tool_calls:
      - {name: read, args: {path: a.js}, id: tc1}
      - {name: read, args: {path: b.js}, id: tc2}
  - on_tool_results: [tc1, tc2]
    then:
      - text: "both done"
`)
	resp1 := postJSON(t, base+"/v1/messages", map[string]any{
		"model":    "faux-1",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	resp1.Body.Close()

	// Second request supplies a tool_result for tc1 only.
	resp2 := postJSON(t, base+"/v1/messages", map[string]any{
		"model": "faux-1",
		"messages": []map[string]any{
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": []map[string]any{
				{"type": "tool_use", "id": "toolu_tc1", "name": "read", "input": map[string]any{}},
				{"type": "tool_use", "id": "toolu_tc2", "name": "read", "input": map[string]any{}},
			}},
			{"role": "user", "content": []map[string]any{
				{"type": "tool_result", "tool_use_id": "toolu_tc1", "content": "a"},
			}},
		},
	})
	resp2.Body.Close()

	st := s.engine.forModel("faux-1").state()
	if len(st.Errors) == 0 {
		t.Fatal("expected a recorded mismatch for the missing tc2 result, got none")
	}
	if !strings.Contains(st.Errors[0], "tc2") {
		t.Errorf("mismatch message = %q, want mention of tc2", st.Errors[0])
	}
}

// --- disconnect_after ------------------------------------------------------

const disconnectLongText = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ this reply is long enough that a small byte cutoff lands partway through it"

func TestDisconnectAfterBytesAnthropicNonStreaming(t *testing.T) {
	s, base := startTestServer(t, `
model: faux-1
steps:
  - text: "`+disconnectLongText+`"
    disconnect_after: 20
  - text: "next step"
`)
	resp := postJSON(t, base+"/v1/messages", map[string]any{
		"model":    "faux-1",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr == nil {
		t.Fatalf("expected a read error from the cut connection, got nil (body = %q)", body)
	}
	if len(body) == 0 {
		t.Error("expected some partial body before the cut, got none")
	}

	st := s.engine.forModel("faux-1").state()
	if st.Disconnects != 1 {
		t.Errorf("Disconnects = %d, want 1", st.Disconnects)
	}
	if st.StepIndex != 1 {
		t.Errorf("StepIndex = %d, want 1 (the cut step still consumed its turn)", st.StepIndex)
	}

	// A retry after the cut lands on the *next* step, not a repeat.
	resp2 := postJSON(t, base+"/v1/messages", map[string]any{
		"model":    "faux-1",
		"messages": []map[string]any{{"role": "user", "content": "retry"}},
	})
	var payload map[string]any
	if err := json.NewDecoder(resp2.Body).Decode(&payload); err != nil {
		t.Fatalf("decode retry response: %v", err)
	}
	resp2.Body.Close()
	content := payload["content"].([]any)
	if content[0].(map[string]any)["text"] != "next step" {
		t.Errorf("retry text = %v, want %q", payload, "next step")
	}
}

func TestDisconnectAfterBytesAnthropicStreaming(t *testing.T) {
	s, base := startTestServer(t, `
model: faux-1
steps:
  - text: "`+disconnectLongText+`"
    disconnect_after: 20
`)
	resp := postJSON(t, base+"/v1/messages", map[string]any{
		"model":  "faux-1",
		"stream": true,
		"messages": []map[string]any{
			{"role": "user", "content": "hi"},
		},
	})
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	sawMessageStop := false
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), `"type":"message_stop"`) {
			sawMessageStop = true
		}
	}
	scanErr := scanner.Err()
	resp.Body.Close()
	if sawMessageStop {
		t.Fatal("stream completed normally (saw message_stop); expected it to be cut short")
	}
	if scanErr == nil {
		t.Error("expected a scan error from the cut connection, got nil")
	}

	st := s.engine.forModel("faux-1").state()
	if st.Disconnects != 1 {
		t.Errorf("Disconnects = %d, want 1", st.Disconnects)
	}
}

func TestDisconnectAfterBytesOpenAIStreaming(t *testing.T) {
	s, base := startTestServer(t, `
model: faux-1
steps:
  - text: "`+disconnectLongText+`"
    disconnect_after: 20
`)
	resp := postJSON(t, base+"/v1/chat/completions", map[string]any{
		"model":  "faux-1",
		"stream": true,
		"messages": []map[string]any{
			{"role": "user", "content": "hi"},
		},
	})
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	sawDone := false
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "[DONE]") {
			sawDone = true
		}
	}
	scanErr := scanner.Err()
	resp.Body.Close()
	if sawDone {
		t.Fatal("stream completed normally (saw [DONE]); expected it to be cut short")
	}
	if scanErr == nil {
		t.Error("expected a scan error from the cut connection, got nil")
	}

	st := s.engine.forModel("faux-1").state()
	if st.Disconnects != 1 {
		t.Errorf("Disconnects = %d, want 1", st.Disconnects)
	}
}

func TestDisconnectAfterDurationCutsBeforeCompletion(t *testing.T) {
	// A 1ms duration is far shorter than it takes to write the whole
	// reply, so the cut fires at or near the very start of the response.
	s, base := startTestServer(t, `
model: faux-1
steps:
  - text: "`+disconnectLongText+`"
    disconnect_after: 1ms
`)
	resp := postJSON(t, base+"/v1/messages", map[string]any{
		"model":    "faux-1",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr == nil {
		t.Fatalf("expected a read error from the cut connection, got nil (body = %q)", body)
	}

	st := s.engine.forModel("faux-1").state()
	if st.Disconnects != 1 {
		t.Errorf("Disconnects = %d, want 1", st.Disconnects)
	}
}

// --- raw_args ----------------------------------------------------------------

const rawArgsText = `{"path": `

func TestRawArgsAnthropicNonStreaming(t *testing.T) {
	_, base := startTestServer(t, `
model: faux-1
steps:
  - tool_call: {name: read, raw_args: '`+rawArgsText+`', id: tc1}
`)
	resp := postJSON(t, base+"/v1/messages", map[string]any{
		"model":    "faux-1",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), `"input":`+rawArgsText) {
		t.Errorf("body = %s, want it to contain the raw text %q verbatim after \"input\":", body, rawArgsText)
	}
	// The body is deliberately invalid JSON.
	var v map[string]any
	if err := json.Unmarshal(body, &v); err == nil {
		t.Errorf("expected invalid JSON body, but it decoded fine: %s", body)
	}
}

func TestRawArgsAnthropicStreaming(t *testing.T) {
	_, base := startTestServer(t, `
model: faux-1
steps:
  - tool_call: {name: read, raw_args: '`+rawArgsText+`', id: tc1}
`)
	resp := postJSON(t, base+"/v1/messages", map[string]any{
		"model":  "faux-1",
		"stream": true,
		"messages": []map[string]any{
			{"role": "user", "content": "hi"},
		},
	})
	defer resp.Body.Close()
	events := readSSE(t, resp.Body)

	var toolInputJSON string
	for _, ev := range events {
		var payload map[string]any
		if err := json.Unmarshal([]byte(ev.data), &payload); err != nil {
			t.Fatalf("bad json in event %q: %v (%s)", ev.event, err, ev.data)
		}
		if payload["type"] != "content_block_delta" {
			continue
		}
		delta := payload["delta"].(map[string]any)
		if delta["type"] == "input_json_delta" {
			toolInputJSON += delta["partial_json"].(string)
		}
	}
	if toolInputJSON != rawArgsText {
		t.Errorf("toolInputJSON = %q, want %q", toolInputJSON, rawArgsText)
	}
}

func TestRawArgsOpenAINonStreaming(t *testing.T) {
	_, base := startTestServer(t, `
model: faux-1
steps:
  - tool_call: {name: read, raw_args: '`+rawArgsText+`', id: tc1}
`)
	resp := postJSON(t, base+"/v1/chat/completions", map[string]any{
		"model":    "faux-1",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	choices := payload["choices"].([]any)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	toolCalls := message["tool_calls"].([]any)
	args := toolCalls[0].(map[string]any)["function"].(map[string]any)["arguments"].(string)
	if args != rawArgsText {
		t.Errorf("arguments = %q, want %q", args, rawArgsText)
	}
}

func TestRawArgsOpenAIStreaming(t *testing.T) {
	_, base := startTestServer(t, `
model: faux-1
steps:
  - tool_call: {name: read, raw_args: '`+rawArgsText+`', id: tc1}
`)
	resp := postJSON(t, base+"/v1/chat/completions", map[string]any{
		"model":  "faux-1",
		"stream": true,
		"messages": []map[string]any{
			{"role": "user", "content": "hi"},
		},
	})
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var toolArgs string
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("bad chunk json: %v (%s)", err, data)
		}
		choice := chunk["choices"].([]any)[0].(map[string]any)
		delta, ok := choice["delta"].(map[string]any)
		if !ok {
			continue
		}
		tcs, ok := delta["tool_calls"].([]any)
		if !ok {
			continue
		}
		for _, tcRaw := range tcs {
			tc := tcRaw.(map[string]any)
			if fn, ok := tc["function"].(map[string]any); ok {
				if a, ok := fn["arguments"].(string); ok {
					toolArgs += a
				}
			}
		}
	}
	if toolArgs != rawArgsText {
		t.Errorf("toolArgs = %q, want %q", toolArgs, rawArgsText)
	}
}

func TestToolCallArgsAndRawArgsMutuallyExclusive(t *testing.T) {
	_, err := New(Options{ScriptYAML: `
model: faux-1
steps:
  - tool_call: {name: read, args: {path: a.js}, raw_args: '{"path": ', id: tc1}
`})
	if err == nil {
		t.Fatal("expected an error loading a script with both args and raw_args set, got nil")
	}
	if !strings.Contains(err.Error(), "args and raw_args") {
		t.Errorf("error = %v, want mention of args and raw_args", err)
	}
}

// --- Main -----------------------------------------------------------

func TestMainInvalidScriptPathReturnsError(t *testing.T) {
	err := Main([]string{"/nonexistent/definitely-not-a-script.yaml"})
	if err == nil {
		t.Fatalf("Main() with nonexistent script path returned nil error, want error")
	}
}

func TestMainInvalidAddrReturnsError(t *testing.T) {
	err := Main([]string{"-addr", "not a valid address"})
	if err == nil {
		t.Fatalf("Main() with invalid -addr returned nil error, want error")
	}
}

func TestMainDefaultServesAndPrintsAddr(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	done := make(chan struct{})
	go func() {
		// Main blocks forever (select{}) once it starts serving; this
		// goroutine is intentionally leaked for the rest of the test
		// binary's life once the address line has been read.
		_ = Main([]string{"-addr", "127.0.0.1:0"})
		close(done)
	}()

	br := bufio.NewReader(r)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("reading address line: %v", err)
	}
	os.Stdout = origStdout
	_ = w.Close()

	addr := strings.TrimSpace(line)
	if addr == "" {
		t.Fatalf("Main() printed empty address")
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		t.Fatalf("Main() printed %q, not a host:port: %v", addr, err)
	}
}
