package faux

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
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

	presentIDs := anthropicToolResultIDs(req.Messages)
	t, exhausted, _ := s.engine.consume(presentIDs)

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

	if d := t.totalDelay(); d > 0 {
		time.Sleep(d)
	}

	msgID := fmt.Sprintf("msg_faux_%d", rec.Seq)
	if req.Stream {
		s.streamAnthropic(w, req.Model, msgID, t)
		return
	}
	s.respondAnthropicJSON(w, req.Model, msgID, t)
}

func exhaustedTurn() turn {
	text := exhaustedText
	return turn{content: []contentStep{{text: &text}}}
}

const exhaustedText = "(faux: script exhausted)"

// --- non-streaming --------------------------------------------------------

func (s *Server) respondAnthropicJSON(w http.ResponseWriter, model, msgID string, t turn) {
	content := []map[string]any{}
	for _, c := range t.content {
		switch {
		case c.text != nil:
			content = append(content, map[string]any{"type": "text", "text": *c.text})
		case c.thinking != nil:
			content = append(content, map[string]any{"type": "thinking", "thinking": *c.thinking, "signature": ""})
		case c.toolCall != nil:
			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    anthropicToolID(c.toolCall.ID),
				"name":  c.toolCall.Name,
				"input": c.toolCall.Args,
			})
		}
	}
	stopReason := "end_turn"
	if t.hasToolCall() {
		stopReason = "tool_use"
	}
	usage := resolveUsage(t.lastUsage())

	writeJSON(w, http.StatusOK, map[string]any{
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
}

func anthropicToolID(id string) string {
	return "toolu_" + id
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

func (s *Server) streamAnthropic(w http.ResponseWriter, model, msgID string, t turn) {
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
			argsJSON, _ := json.Marshal(c.toolCall.Args)
			for _, chunk := range chunkString(string(argsJSON), chunkSize) {
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
		}
	}

	stopReason := "end_turn"
	if t.hasToolCall() {
		stopReason = "tool_use"
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
