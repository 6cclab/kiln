package faux

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// --- request shapes -----------------------------------------------------

type anthropicContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

type anthropicRequest struct {
	Model    string             `json:"model"`
	System   json.RawMessage    `json:"system,omitempty"`
	Messages []anthropicMessage `json:"messages"`
	Tools    []anthropicTool    `json:"tools,omitempty"`
	Stream   bool               `json:"stream,omitempty"`
}

func anthropicSystemText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []anthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		out := ""
		for _, b := range blocks {
			out += b.Text
		}
		return out
	}
	return ""
}

// anthropicToolResultIDs scans a request's messages for tool_result
// blocks and returns the set of tool_use_id values present.
func anthropicToolResultIDs(messages []anthropicMessage) map[string]bool {
	ids := map[string]bool{}
	for _, m := range messages {
		var blocks []anthropicContentBlock
		if err := json.Unmarshal(m.Content, &blocks); err != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type == "tool_result" && b.ToolUseID != "" {
				ids[b.ToolUseID] = true
			}
		}
	}
	return ids
}

// --- handler -------------------------------------------------------------

func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"type": "error", "error": map[string]string{"type": "invalid_request_error", "message": err.Error()}})
		return
	}

	var req anthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"type": "error", "error": map[string]string{"type": "invalid_request_error", "message": err.Error()}})
		return
	}

	tools := make([]ToolSpec, 0, len(req.Tools))
	for _, t := range req.Tools {
		tools = append(tools, ToolSpec{Name: t.Name, Schema: t.InputSchema})
	}
	messagesJSON, _ := json.Marshal(req.Messages)
	rec := s.rec.record(Request{
		Time:     time.Now(),
		Method:   r.Method,
		Path:     r.URL.Path,
		Protocol: "anthropic",
		Model:    req.Model,
		System:   anthropicSystemText(req.System),
		Tools:    tools,
		Messages: messagesJSON,
		Stream:   req.Stream,
		Body:     body,
	})

	me := s.engine.forModel(req.Model)
	if me == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"type": "error",
			"error": map[string]string{
				"type":    "invalid_request_error",
				"message": fmt.Sprintf("faux: no script for model %q (scripted models: %s)", req.Model, strings.Join(s.engine.modelNames(), ", ")),
			},
		})
		return
	}

	presentIDs := anthropicToolResultIDs(req.Messages)
	t, exhausted, _ := me.consume(presentIDs)

	if t.isError {
		writeJSON(w, t.errSpec.Status, map[string]any{
			"type": "error",
			"error": map[string]string{
				"type":    t.errSpec.Type,
				"message": t.errSpec.Message,
			},
		})
		return
	}

	if exhausted {
		t = exhaustedTurn()
	}

	if d := t.totalDelay(); d > 0 && !sleepOrGone(r, d) {
		return // the client hung up mid-delay (a cancelled request)
	}

	msgID := fmt.Sprintf("msg_faux_%d", rec.Seq)
	spec := t.disconnectSpec()
	if req.Stream {
		s.streamAnthropic(w, req.Model, msgID, t, spec, me.recordDisconnect)
		return
	}
	s.respondAnthropicJSON(w, req.Model, msgID, t, spec, me.recordDisconnect)
}

func exhaustedTurn() turn {
	text := exhaustedText
	return turn{content: []contentStep{{text: &text}}}
}

const exhaustedText = "(faux: script exhausted)"

// --- non-streaming --------------------------------------------------------

// rawArgsSplice records a placeholder token planted in a JSON body for a
// tool call's raw_args, so it can be replaced with the raw text after
// marshaling (encoding/json cannot emit invalid JSON directly; see
// raw_args in the package doc).
type rawArgsSplice struct {
	placeholder string
	raw         string
}

func rawArgsPlaceholder(i int) string {
	return fmt.Sprintf("__faux_raw_args_%d__", i)
}

func (s *Server) respondAnthropicJSON(w http.ResponseWriter, model, msgID string, t turn, spec *disconnectSpec, onCut func()) {
	content := []map[string]any{}
	var splices []rawArgsSplice
	for _, c := range t.content {
		switch {
		case c.text != nil:
			content = append(content, map[string]any{"type": "text", "text": *c.text})
		case c.thinking != nil:
			content = append(content, map[string]any{"type": "thinking", "thinking": *c.thinking, "signature": ""})
		case c.toolCall != nil:
			var input any = c.toolCall.Args
			if c.toolCall.RawArgs != "" {
				placeholder := rawArgsPlaceholder(len(splices))
				splices = append(splices, rawArgsSplice{placeholder: placeholder, raw: c.toolCall.RawArgs})
				input = placeholder
			}
			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    anthropicToolID(c.toolCall.ID),
				"name":  c.toolCall.Name,
				"input": input,
			})
		case c.rawBlock != nil:
			content = append(content, c.rawBlock)
		}
	}
	stopReason := "end_turn"
	if t.hasToolCall() {
		stopReason = "tool_use"
	}
	if o := t.stopReasonOverride(); o != "" {
		stopReason = o
	}
	usage := resolveUsage(t.lastUsage())

	body, _ := json.Marshal(map[string]any{
		"id":            msgID,
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       content,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  usage.Input,
			"output_tokens": usage.Output,
		},
	})
	for _, sp := range splices {
		body = bytes.Replace(body, []byte(`"`+sp.placeholder+`"`), []byte(sp.raw), 1)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	writeWithDisconnect(w, body, spec, onCut)
}

func anthropicToolID(id string) string {
	return "toolu_" + id
}

// toolCallArgsJSON returns the JSON text to use for a tool call's
// arguments: the spec's RawArgs verbatim if set (letting a script emit
// intentionally invalid JSON, since it's carried as plain text in both
// the Anthropic streaming partial_json chunks and the OpenAI
// function.arguments string field), or Args marshaled normally otherwise.
func toolCallArgsJSON(spec *ToolCallSpec) string {
	if spec.RawArgs != "" {
		return spec.RawArgs
	}
	b, _ := json.Marshal(spec.Args)
	return string(b)
}

func resolveUsage(u *UsageSpec) UsageSpec {
	if u != nil {
		return *u
	}
	return UsageSpec{Input: 100, Output: 50}
}

// --- streaming -------------------------------------------------------------

const chunkSize = 8

func chunkString(s string, size int) []string {
	if s == "" {
		return nil
	}
	var out []string
	rs := []rune(s)
	for i := 0; i < len(rs); i += size {
		end := i + size
		if end > len(rs) {
			end = len(rs)
		}
		out = append(out, string(rs[i:end]))
	}
	return out
}

type sseWriter struct {
	w  http.ResponseWriter
	bw *bufio.Writer
	f  http.Flusher
}

func newSSEWriter(w http.ResponseWriter) *sseWriter {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	f, _ := w.(http.Flusher)
	return &sseWriter{w: w, bw: bufio.NewWriter(w), f: f}
}

func (sw *sseWriter) send(event string, data any) {
	b, _ := json.Marshal(data)
	if event != "" {
		fmt.Fprintf(sw.bw, "event: %s\n", event)
	}
	fmt.Fprintf(sw.bw, "data: %s\n\n", b)
	sw.bw.Flush()
	if sw.f != nil {
		sw.f.Flush()
	}
}

func (sw *sseWriter) sendRaw(data string) {
	fmt.Fprintf(sw.bw, "data: %s\n\n", data)
	sw.bw.Flush()
	if sw.f != nil {
		sw.f.Flush()
	}
}

func (s *Server) streamAnthropic(w http.ResponseWriter, model, msgID string, t turn, spec *disconnectSpec, onCut func()) {
	if spec != nil {
		w = newDisconnectWriter(w, spec, onCut)
	}
	sw := newSSEWriter(w)
	w.WriteHeader(http.StatusOK)

	usage := resolveUsage(t.lastUsage())

	sw.send("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            msgID,
			"type":          "message",
			"role":          "assistant",
			"model":         model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  usage.Input,
				"output_tokens": 0,
			},
		},
	})

	index := 0
	for _, c := range t.content {
		switch {
		case c.text != nil:
			sw.send("content_block_start", map[string]any{
				"type":  "content_block_start",
				"index": index,
				"content_block": map[string]any{
					"type": "text",
					"text": "",
				},
			})
			for _, chunk := range chunkString(*c.text, chunkSize) {
				sw.send("content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": index,
					"delta": map[string]any{
						"type": "text_delta",
						"text": chunk,
					},
				})
				time.Sleep(c.chunkDelay)
			}
			sw.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
			index++

		case c.thinking != nil:
			sw.send("content_block_start", map[string]any{
				"type":  "content_block_start",
				"index": index,
				"content_block": map[string]any{
					"type":     "thinking",
					"thinking": "",
				},
			})
			for _, chunk := range chunkString(*c.thinking, chunkSize) {
				sw.send("content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": index,
					"delta": map[string]any{
						"type":     "thinking_delta",
						"thinking": chunk,
					},
				})
			}
			sw.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
			index++

		case c.toolCall != nil:
			id := anthropicToolID(c.toolCall.ID)
			sw.send("content_block_start", map[string]any{
				"type":  "content_block_start",
				"index": index,
				"content_block": map[string]any{
					"type":  "tool_use",
					"id":    id,
					"name":  c.toolCall.Name,
					"input": map[string]any{},
				},
			})
			argsJSON := toolCallArgsJSON(c.toolCall)
			for _, chunk := range chunkString(argsJSON, chunkSize) {
				sw.send("content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": index,
					"delta": map[string]any{
						"type":         "input_json_delta",
						"partial_json": chunk,
					},
				})
			}
			sw.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
			index++

		case c.rawBlock != nil:
			blockType, _ := c.rawBlock["type"].(string)
			if blockType == "server_tool_use" {
				input, hasInput := c.rawBlock["input"]
				start := map[string]any{}
				for k, v := range c.rawBlock {
					if k != "input" {
						start[k] = v
					}
				}
				start["input"] = map[string]any{}
				sw.send("content_block_start", map[string]any{
					"type": "content_block_start", "index": index, "content_block": start,
				})
				if hasInput {
					inputJSON, _ := json.Marshal(input)
					for _, chunk := range chunkString(string(inputJSON), chunkSize) {
						sw.send("content_block_delta", map[string]any{
							"type": "content_block_delta", "index": index,
							"delta": map[string]any{"type": "input_json_delta", "partial_json": chunk},
						})
					}
				}
			} else {
				// Complete blocks (e.g. web_search_tool_result) send their
				// whole content in content_block_start, with no deltas,
				// matching the real API.
				sw.send("content_block_start", map[string]any{
					"type": "content_block_start", "index": index, "content_block": c.rawBlock,
				})
			}
			sw.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
			index++
		}
	}

	stopReason := "end_turn"
	if t.hasToolCall() {
		stopReason = "tool_use"
	}
	if o := t.stopReasonOverride(); o != "" {
		stopReason = o
	}
	sw.send("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": map[string]any{
			"input_tokens":  usage.Input,
			"output_tokens": usage.Output,
		},
	})
	sw.send("message_stop", map[string]any{"type": "message_stop"})
}
