package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// OpenAI chat-completions API streaming client, ported from pi-ai's
// dist/api/openai-completions.js. Covers request construction (system/
// developer role, tool_result -> role:"tool" mapping, image content parts,
// reasoning_effort for thinkingFormat "openai", stream_options.include_usage,
// max_tokens/max_completion_tokens via compat.maxTokensField) and response
// parsing (usage, finish_reason mapping, reasoning_content deltas, tool call
// argument accumulation). The provider-specific thinkingFormat variants
// (deepseek/together/baseten/zai/qwen/chat-template/...) at
// openai-completions.js:600-720 are out of scope this phase; only "openai"
// (reasoning_effort) is implemented, noted as a deviation in the phase-2
// report.

// OpenAICompletionsClient streams completions against one OpenAI
// chat-completions-shaped endpoint (model.BaseURL + "/chat/completions").
type OpenAICompletionsClient struct {
	HTTPClient *http.Client
}

func (c *OpenAICompletionsClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// --- wire request shapes ---

type openAIContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *openAIImageURL `json:"image_url,omitempty"`
}

type openAIImageURL struct {
	URL string `json:"url"`
}

type openAIWireToolCall struct {
	Index    int                `json:"index,omitempty"`
	ID       string             `json:"id,omitempty"`
	Type     string             `json:"type,omitempty"`
	Function openAIWireFunction `json:"function"`
}

type openAIWireFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

type openAIWireMessage struct {
	Role       string               `json:"role"`
	Content    interface{}          `json:"content,omitempty"`
	Name       string               `json:"name,omitempty"`
	ToolCallID string               `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIWireToolCall `json:"tool_calls,omitempty"`
}

type openAIWireTool struct {
	Type     string                `json:"type"`
	Function openAIWireFunctionDef `json:"function"`
}

type openAIWireFunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      bool            `json:"strict,omitempty"`
}

type openAIStreamOptionsWire struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIRequest struct {
	Model               string                   `json:"model"`
	Messages            []openAIWireMessage      `json:"messages"`
	Tools               []openAIWireTool         `json:"tools,omitempty"`
	Stream              bool                     `json:"stream"`
	StreamOptions       *openAIStreamOptionsWire `json:"stream_options,omitempty"`
	MaxTokens           int                      `json:"max_tokens,omitempty"`
	MaxCompletionTokens int                      `json:"max_completion_tokens,omitempty"`
	Temperature         *float64                 `json:"temperature,omitempty"`
	ReasoningEffort     string                   `json:"reasoning_effort,omitempty"`
}

func buildOpenAIRequest(model provider.Model, transcript []msg.Message, opts provider.StreamOptions) openAIRequest {
	compat := model.OpenAICompletionsCompat()

	req := openAIRequest{Model: model.ID, Stream: true}
	if boolDefault(compat.SupportsUsageInStreaming, true) {
		req.StreamOptions = &openAIStreamOptionsWire{IncludeUsage: true}
	}

	maxTokens := model.MaxTokens
	if opts.MaxTokens > 0 {
		maxTokens = opts.MaxTokens
	}
	if maxTokens > 0 {
		if model.MaxTokensField() == "max_completion_tokens" {
			req.MaxCompletionTokens = maxTokens
		} else {
			req.MaxTokens = maxTokens
		}
	}

	systemRole := "system"
	if model.Reasoning && model.SupportsDeveloperRole() {
		systemRole = "developer"
	}

	var systemText string
	for _, m := range transcript {
		if sm, ok := m.(msg.SystemMessage); ok {
			systemText = msg.TextOf(sm.Content)
			break
		}
	}
	if opts.SystemPrompt != "" {
		systemText = opts.SystemPrompt
	}
	if systemText != "" {
		req.Messages = append(req.Messages, openAIWireMessage{Role: systemRole, Content: systemText})
	}

	for _, m := range transcript {
		switch t := m.(type) {
		case msg.SystemMessage:
			// folded above
		case msg.UserMessage:
			req.Messages = append(req.Messages, openAIWireMessage{Role: "user", Content: convertBlocksToOpenAI(t.Content)})
		case msg.AssistantMessage:
			wm := openAIWireMessage{Role: "assistant"}
			var text string
			var calls []openAIWireToolCall
			for _, b := range t.Content {
				switch c := b.(type) {
				case msg.TextContent:
					text += c.Text
				case msg.ToolCall:
					args, _ := json.Marshal(c.Arguments)
					calls = append(calls, openAIWireToolCall{ID: c.ID, Type: "function", Function: openAIWireFunction{Name: c.Name, Arguments: string(args)}})
				}
			}
			if text != "" {
				wm.Content = text
			}
			if len(calls) > 0 {
				wm.ToolCalls = calls
			}
			req.Messages = append(req.Messages, wm)
		case msg.ToolResultMessage:
			req.Messages = append(req.Messages, openAIWireMessage{
				Role:       "tool",
				Content:    msg.TextOf(t.Content),
				ToolCallID: t.ToolCallID,
				Name:       t.ToolName,
			})
		}
	}

	for _, td := range opts.Tools {
		schema := td.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		req.Tools = append(req.Tools, openAIWireTool{
			Type: "function",
			Function: openAIWireFunctionDef{
				Name:        td.Name,
				Description: td.Description,
				Parameters:  schema,
				Strict:      model.SupportsStrictMode(),
			},
		})
	}

	if opts.Temperature != nil {
		req.Temperature = opts.Temperature
	}

	if model.Reasoning && opts.ThinkingLevel != "" && opts.ThinkingLevel != provider.ThinkingOff && model.SupportsReasoningEffort() {
		effort := string(opts.ThinkingLevel)
		if model.ThinkingLevelMap != nil {
			if mapped, ok := model.ThinkingLevelMap[opts.ThinkingLevel]; ok && mapped != nil {
				effort = *mapped
			}
		}
		req.ReasoningEffort = effort
	}

	return req
}

func convertBlocksToOpenAI(blocks msg.Blocks) interface{} {
	hasImage := false
	for _, b := range blocks {
		if _, ok := b.(msg.ImageContent); ok {
			hasImage = true
			break
		}
	}
	if !hasImage {
		return msg.TextOf(blocks)
	}
	parts := make([]openAIContentPart, 0, len(blocks))
	for _, b := range blocks {
		switch c := b.(type) {
		case msg.TextContent:
			parts = append(parts, openAIContentPart{Type: "text", Text: c.Text})
		case msg.ImageContent:
			parts = append(parts, openAIContentPart{Type: "image_url", ImageURL: &openAIImageURL{URL: "data:" + c.MimeType + ";base64," + c.Data}})
		}
	}
	return parts
}

func openAIHeaders(model provider.Model, auth Auth) http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Authorization", "Bearer "+auth.APIKey)
	for k, v := range model.Headers {
		if v == "" {
			h.Del(k)
		} else {
			h.Set(k, v)
		}
	}
	for k, v := range auth.Headers {
		h.Set(k, v)
	}
	return h
}

// --- response wire shapes ---

type openAIChunk struct {
	Choices []struct {
		Delta struct {
			Role             string               `json:"role"`
			Content          string               `json:"content"`
			ReasoningContent string               `json:"reasoning_content"`
			Reasoning        string               `json:"reasoning"`
			ToolCalls        []openAIWireToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

func mapOpenAIStopReason(reason string) (msg.StopReason, string) {
	switch reason {
	case "", "stop", "end":
		return msg.StopStop, ""
	case "length":
		return msg.StopLength, ""
	case "function_call", "tool_calls":
		return msg.StopToolUse, ""
	case "content_filter":
		return msg.StopError, "Provider finish_reason: content_filter"
	case "network_error":
		return msg.StopError, "Provider finish_reason: network_error"
	default:
		return msg.StopError, fmt.Sprintf("Provider finish_reason: %s", reason)
	}
}

// Stream starts an OpenAI chat-completions completion.
func (c *OpenAICompletionsClient) Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions, auth Auth) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	events := make(chan msg.StreamEvent, 16)
	done := make(chan struct{})
	var final *msg.AssistantMessage
	var finalErr error

	go func() {
		defer close(events)
		defer close(done)
		final, finalErr = c.run(ctx, model, transcript, opts, auth, events)
	}()

	wait := func() (*msg.AssistantMessage, error) {
		<-done
		return final, finalErr
	}
	return events, wait
}

func (c *OpenAICompletionsClient) run(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions, auth Auth, events chan<- msg.StreamEvent) (*msg.AssistantMessage, error) {
	partial := &msg.AssistantMessage{
		API:        string(provider.ApiOpenAICompletions),
		Provider:   model.Provider,
		Model:      model.ID,
		Role:       msg.RoleAssistant,
		StopReason: msg.StopPending,
		Content:    msg.Blocks{},
	}

	wireReq := buildOpenAIRequest(model, transcript, opts)
	body, err := json.Marshal(wireReq)
	if err != nil {
		return errorOut(partial, events, false, err)
	}

	url := strings.TrimRight(model.BaseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return errorOut(partial, events, false, err)
	}
	httpReq.Header = openAIHeaders(model, auth)

	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return errorOut(partial, events, ctx.Err() != nil, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		buf, _ := io.ReadAll(resp.Body)
		return errorOut(partial, events, isRetriableStatus(resp.StatusCode), &StatusError{Status: resp.StatusCode, Body: string(buf), Retriable: isRetriableStatus(resp.StatusCode)})
	}

	events <- msg.StreamEvent{Type: msg.EventStart, Partial: partial}

	var textIndex = -1
	var thinkingIndex = -1
	toolIndexOf := map[int]int{}    // openai tool-call index -> partial.Content position
	toolArgsBuf := map[int]string{} // openai tool-call index -> accumulated raw JSON
	sawFinish := false

	r := bufio.NewReader(resp.Body)
	streamErr := scanSSE(r, func(ev sseEvent) {
		data := strings.TrimSpace(ev.Data)
		if data == "" {
			return
		}
		if data == "[DONE]" {
			return
		}
		var chunk openAIChunk
		if json.Unmarshal([]byte(data), &chunk) != nil {
			return
		}
		if chunk.Usage != nil {
			partial.Usage.Input = chunk.Usage.PromptTokens
			partial.Usage.Output = chunk.Usage.CompletionTokens
			partial.Usage.TotalTokens = chunk.Usage.TotalTokens
			computeAnthropicCost(model, &partial.Usage) // rate math is provider-agnostic
		}
		if len(chunk.Choices) == 0 {
			return
		}
		choice := chunk.Choices[0]
		delta := choice.Delta

		if delta.Content != "" {
			if textIndex == -1 {
				partial.Content = append(partial.Content, msg.Text(""))
				textIndex = len(partial.Content) - 1
				events <- msg.StreamEvent{Type: msg.EventTextStart, ContentIndex: textIndex, Partial: partial}
			}
			tc := partial.Content[textIndex].(msg.TextContent)
			tc.Text += delta.Content
			partial.Content[textIndex] = tc
			events <- msg.StreamEvent{Type: msg.EventTextDelta, ContentIndex: textIndex, Delta: delta.Content, Partial: partial}
		}

		reasoning := delta.ReasoningContent
		if reasoning == "" {
			reasoning = delta.Reasoning
		}
		if reasoning != "" {
			if thinkingIndex == -1 {
				partial.Content = append(partial.Content, msg.Thinking(""))
				thinkingIndex = len(partial.Content) - 1
				events <- msg.StreamEvent{Type: msg.EventThinkingStart, ContentIndex: thinkingIndex, Partial: partial}
			}
			tc := partial.Content[thinkingIndex].(msg.ThinkingContent)
			tc.Thinking += reasoning
			partial.Content[thinkingIndex] = tc
			events <- msg.StreamEvent{Type: msg.EventThinkingDelta, ContentIndex: thinkingIndex, Delta: reasoning, Partial: partial}
		}

		for _, tc := range delta.ToolCalls {
			pos, ok := toolIndexOf[tc.Index]
			if !ok {
				partial.Content = append(partial.Content, msg.NewToolCall(tc.ID, tc.Function.Name, nil))
				pos = len(partial.Content) - 1
				toolIndexOf[tc.Index] = pos
				events <- msg.StreamEvent{Type: msg.EventToolCallStart, ContentIndex: pos, Partial: partial}
			}
			if tc.Function.Arguments != "" {
				toolArgsBuf[tc.Index] += tc.Function.Arguments
				call := partial.Content[pos].(msg.ToolCall)
				var args map[string]any
				if json.Unmarshal([]byte(toolArgsBuf[tc.Index]), &args) == nil {
					call.Arguments = args
				}
				partial.Content[pos] = call
				events <- msg.StreamEvent{Type: msg.EventToolCallDelta, ContentIndex: pos, Delta: tc.Function.Arguments, Partial: partial}
			}
		}

		if choice.FinishReason != "" {
			sawFinish = true
			partial.RawStopReason = choice.FinishReason
			reason, errMsg := mapOpenAIStopReason(choice.FinishReason)
			partial.StopReason = reason
			if errMsg != "" {
				partial.ErrorMessage = errMsg
			}
		}
	})

	// Finalize tool call arguments: parse each accumulated argument buffer
	// now that all deltas are in, and emit toolcall_end.
	if streamErr != io.EOF {
		return errorOut(partial, events, ctx.Err() != nil, streamErr)
	}

	for _, pos := range toolIndexOf {
		call := partial.Content[pos].(msg.ToolCall)
		events <- msg.StreamEvent{Type: msg.EventToolCallEnd, ContentIndex: pos, ToolCall: &call, Partial: partial}
	}
	if textIndex != -1 {
		tc := partial.Content[textIndex].(msg.TextContent)
		events <- msg.StreamEvent{Type: msg.EventTextEnd, ContentIndex: textIndex, Content: tc.Text, Partial: partial}
	}
	if thinkingIndex != -1 {
		tc := partial.Content[thinkingIndex].(msg.ThinkingContent)
		events <- msg.StreamEvent{Type: msg.EventThinkingEnd, ContentIndex: thinkingIndex, Content: tc.Thinking, Partial: partial}
	}

	if ctx.Err() != nil {
		return errorOut(partial, events, true, fmt.Errorf("request was aborted"))
	}
	if !sawFinish {
		return errorOut(partial, events, false, fmt.Errorf("stream ended without finish_reason"))
	}
	if partial.StopReason == msg.StopAborted || partial.StopReason == msg.StopError {
		errMsg := partial.ErrorMessage
		if errMsg == "" {
			errMsg = "an unknown error occurred"
		}
		events <- msg.StreamEvent{Type: msg.EventError, Reason: partial.StopReason, Error: partial}
		return nil, errors.New(errMsg)
	}

	events <- msg.StreamEvent{Type: msg.EventDone, Reason: partial.StopReason, Message: partial}
	return partial, nil
}
