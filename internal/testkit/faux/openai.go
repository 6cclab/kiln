package faux

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// --- request shapes -----------------------------------------------------

type openAIFunctionSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type openAIToolSpec struct {
	Type     string             `json:"type"`
	Function openAIFunctionSpec `json:"function"`
}

type openAIMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
}

type openAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

type openAIRequest struct {
	Model         string               `json:"model"`
	Messages      []openAIMessage      `json:"messages"`
	Tools         []openAIToolSpec     `json:"tools,omitempty"`
	Stream        bool                 `json:"stream,omitempty"`
	StreamOptions *openAIStreamOptions `json:"stream_options,omitempty"`
}

func openAISystemText(messages []openAIMessage) string {
	for _, m := range messages {
		if m.Role == "system" || m.Role == "developer" {
			var s string
			if err := json.Unmarshal(m.Content, &s); err == nil {
				return s
			}
		}
	}
	return ""
}

// openAIToolResultIDs returns the set of tool_call_id values present in
// role:"tool" messages.
func openAIToolResultIDs(messages []openAIMessage) map[string]bool {
	ids := map[string]bool{}
	for _, m := range messages {
		if m.Role == "tool" && m.ToolCallID != "" {
			ids[m.ToolCallID] = true
		}
	}
	return ids
}

// --- handler ---------------------------------------------------------------

func (s *Server) handleOpenAIChatCompletions(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"type": "invalid_request_error", "message": err.Error()}})
		return
	}

	var req openAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"type": "invalid_request_error", "message": err.Error()}})
		return
	}

	tools := make([]ToolSpec, 0, len(req.Tools))
	for _, t := range req.Tools {
		tools = append(tools, ToolSpec{Name: t.Function.Name, Schema: t.Function.Parameters})
	}
	messagesJSON, _ := json.Marshal(req.Messages)
	rec := s.rec.record(Request{
		Time:     time.Now(),
		Method:   r.Method,
		Path:     r.URL.Path,
		Protocol: "openai",
		Model:    req.Model,
		System:   openAISystemText(req.Messages),
		Tools:    tools,
		Messages: messagesJSON,
		Stream:   req.Stream,
		Body:     body,
	})

	me := s.engine.forModel(req.Model)
	if me == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{
				"type":    "invalid_request_error",
				"message": fmt.Sprintf("faux: no script for model %q (scripted models: %s)", req.Model, strings.Join(s.engine.modelNames(), ", ")),
			},
		})
		return
	}

	presentIDs := openAIToolResultIDs(req.Messages)
	t, exhausted, _ := me.consume(presentIDs)

	if t.isError {
		writeJSON(w, t.errSpec.Status, map[string]any{
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

	chatID := fmt.Sprintf("chatcmpl_faux_%d", rec.Seq)
	includeUsage := req.StreamOptions != nil && req.StreamOptions.IncludeUsage
	if req.Stream {
		s.streamOpenAI(w, req.Model, chatID, t, includeUsage)
		return
	}
	s.respondOpenAIJSON(w, req.Model, chatID, t)
}

func openAICallID(id string) string {
	return "call_" + id
}

// --- non-streaming -----------------------------------------------------

func (s *Server) respondOpenAIJSON(w http.ResponseWriter, model, chatID string, t turn) {
	var text, thinking string
	var toolCalls []map[string]any
	for _, c := range t.content {
		switch {
		case c.text != nil:
			text += *c.text
		case c.thinking != nil:
			thinking += *c.thinking
		case c.toolCall != nil:
			argsJSON, _ := json.Marshal(c.toolCall.Args)
			toolCalls = append(toolCalls, map[string]any{
				"id":   openAICallID(c.toolCall.ID),
				"type": "function",
				"function": map[string]any{
					"name":      c.toolCall.Name,
					"arguments": string(argsJSON),
				},
			})
		}
	}

	message := map[string]any{
		"role":    "assistant",
		"content": nullableString(text),
	}
	if thinking != "" {
		message["reasoning_content"] = thinking
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}

	finishReason := "stop"
	if t.hasToolCall() {
		finishReason = "tool_calls"
	}
	usage := resolveUsage(t.lastUsage())

	writeJSON(w, http.StatusOK, map[string]any{
		"id":      chatID,
		"object":  "chat.completion",
		"model":   model,
		"choices": []map[string]any{{"index": 0, "message": message, "finish_reason": finishReason}},
		"usage": map[string]any{
			"prompt_tokens":     usage.Input,
			"completion_tokens": usage.Output,
			"total_tokens":      usage.Input + usage.Output,
		},
	})
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// --- streaming -----------------------------------------------------------

func (s *Server) streamOpenAI(w http.ResponseWriter, model, chatID string, t turn, includeUsage bool) {
	sw := newSSEWriter(w)
	w.WriteHeader(http.StatusOK)

	base := func(delta map[string]any, finishReason any) map[string]any {
		return map[string]any{
			"id":      chatID,
			"object":  "chat.completion.chunk",
			"model":   model,
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finishReason}},
		}
	}

	sw.sendChunk(base(map[string]any{"role": "assistant"}, nil))

	toolIndex := 0
	for _, c := range t.content {
		switch {
		case c.text != nil:
			for _, chunk := range chunkString(*c.text, chunkSize) {
				sw.sendChunk(base(map[string]any{"content": chunk}, nil))
			}
		case c.thinking != nil:
			for _, chunk := range chunkString(*c.thinking, chunkSize) {
				sw.sendChunk(base(map[string]any{"reasoning_content": chunk}, nil))
			}
		case c.toolCall != nil:
			id := openAICallID(c.toolCall.ID)
			sw.sendChunk(base(map[string]any{
				"tool_calls": []map[string]any{{
					"index": toolIndex,
					"id":    id,
					"type":  "function",
					"function": map[string]any{
						"name":      c.toolCall.Name,
						"arguments": "",
					},
				}},
			}, nil))
			argsJSON, _ := json.Marshal(c.toolCall.Args)
			for _, chunk := range chunkString(string(argsJSON), chunkSize) {
				sw.sendChunk(base(map[string]any{
					"tool_calls": []map[string]any{{
						"index": toolIndex,
						"function": map[string]any{
							"arguments": chunk,
						},
					}},
				}, nil))
			}
			toolIndex++
		}
	}

	finishReason := "stop"
	if t.hasToolCall() {
		finishReason = "tool_calls"
	}
	final := base(map[string]any{}, finishReason)
	if includeUsage {
		usage := resolveUsage(t.lastUsage())
		final["usage"] = map[string]any{
			"prompt_tokens":     usage.Input,
			"completion_tokens": usage.Output,
			"total_tokens":      usage.Input + usage.Output,
		}
	}
	sw.sendChunk(final)
	sw.sendRaw("[DONE]")
}

func (sw *sseWriter) sendChunk(v any) {
	b, _ := json.Marshal(v)
	sw.sendRaw(string(b))
}
