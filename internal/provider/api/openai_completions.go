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

	// Thinking carries the wire shape for thinkingFormat "zai"
	// ({"type":"enabled"|"disabled", ...}), "deepseek" ({"type":"enabled"|"disabled"})
	// or "string-thinking" (a bare string level, e.g. "high"). The shape
	// differs per format, so this is built as raw JSON rather than a typed
	// field; see openai-completions.js:619-712.
	Thinking json.RawMessage `json:"thinking,omitempty"`
	// EnableThinking is thinkingFormat "qwen"'s top-level boolean
	// (openai-completions.js:630-638).
	EnableThinking *bool `json:"enable_thinking,omitempty"`
	// ChatTemplateKwargs is thinkingFormat "chat-template" and
	// "qwen-chat-template"'s `chat_template_kwargs` object
	// (openai-completions.js:639-650).
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`
	// ChatTemplateArgs is thinkingFormat "baseten"'s `chat_template_args`
	// object (openai-completions.js:651-665).
	ChatTemplateArgs map[string]any `json:"chat_template_args,omitempty"`
	// Reasoning carries thinkingFormat "ant-ling"'s `{"effort": ...}` or
	// "together"'s `{"enabled": bool}` shape (openai-completions.js:678-703);
	// raw JSON since the two formats disagree on the object's fields.
	Reasoning json.RawMessage `json:"reasoning,omitempty"`
	// ToolStream is z.ai's top-level `tool_stream: true` for streaming tool
	// call deltas, gated on compat.ZaiToolStream (openai-completions.js:600-603).
	ToolStream *bool `json:"tool_stream,omitempty"`
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
		if len(td.ServerTool) > 0 {
			// A provider-native tool declaration (e.g. Anthropic's
			// web_search): this API has no equivalent, so the tool is
			// simply not offered, matching every non-Anthropic client.
			continue
		}
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
		if compat.ZaiToolStream != nil && *compat.ZaiToolStream {
			req.ToolStream = boolPtr(true)
		}
	}

	if opts.Temperature != nil {
		req.Temperature = opts.Temperature
	}

	applyThinkingFormat(model, opts, compat, &req)

	return req
}

// wantReasoning reports pi's `options?.reasoningEffort` truthiness check:
// a thinking level was explicitly requested and isn't "off".
func wantReasoning(opts provider.StreamOptions) bool {
	return opts.ThinkingLevel != "" && opts.ThinkingLevel != provider.ThinkingOff
}

// mappedThinkingLevel resolves opts.ThinkingLevel (or, when off/unset, the
// model's mapped "off" value) through model.ThinkingLevelMap, matching pi's
// `model.thinkingLevelMap?.[level] ?? level` idiom used throughout
// openai-completions.js's reasoning block. ok is false when the map has an
// explicit null entry for the level (pi's `!== null` guards), meaning the
// field should be omitted entirely.
func mappedThinkingLevel(model provider.Model, level provider.ThinkingLevel) (value string, ok bool) {
	if model.ThinkingLevelMap != nil {
		if mapped, present := model.ThinkingLevelMap[level]; present {
			if mapped == nil {
				return "", false
			}
			return *mapped, true
		}
	}
	return string(level), true
}

// applyThinkingFormat ports openai-completions.js:600-712's thinkingFormat
// switch, faithfully implementing the "deepseek", "together", "baseten",
// "zai", "qwen", "qwen-chat-template", "chat-template", "string-thinking"
// and "ant-ling" variants (plus the default "openai" reasoning_effort path,
// already handled by the caller before this switch runs -- see below). Out
// of scope: "openrouter" thinkingFormat and the top-level thinking-token-
// budget cap (openai-completions.js:730-747's resolveClampedThinkingBudget
// depends on options.thinkingBudgets, which this harness's StreamOptions
// does not carry) -- noted as a deviation in the phase report.
func applyThinkingFormat(model provider.Model, opts provider.StreamOptions, compat provider.OpenAICompletionsCompat, req *openAIRequest) {
	if !model.Reasoning {
		return
	}
	reasoning := wantReasoning(opts)
	supportsEffort := model.SupportsReasoningEffort()

	switch compat.ThinkingFormat {
	case "zai":
		// openai-completions.js:619-629:
		//   zaiParams.thinking = options?.reasoningEffort ? { type: "enabled", clear_thinking: false } : { type: "disabled" };
		//   if (options?.reasoningEffort && compat.supportsReasoningEffort) { ... zaiParams.reasoning_effort = effort; }
		if reasoning {
			req.Thinking, _ = json.Marshal(map[string]any{"type": "enabled", "clear_thinking": false})
		} else {
			req.Thinking, _ = json.Marshal(map[string]any{"type": "disabled"})
		}
		if reasoning && supportsEffort {
			if effort, ok := mappedThinkingLevel(model, opts.ThinkingLevel); ok {
				req.ReasoningEffort = effort
			}
		}
	case "qwen":
		// openai-completions.js:630-638:
		//   params.enable_thinking = !!options?.reasoningEffort;
		//   if (options?.reasoningEffort && compat.supportsReasoningEffort) { ... params.reasoning_effort = effort; }
		req.EnableThinking = boolPtr(reasoning)
		if reasoning && supportsEffort {
			if effort, ok := mappedThinkingLevel(model, opts.ThinkingLevel); ok {
				req.ReasoningEffort = effort
			}
		}
	case "qwen-chat-template":
		// openai-completions.js:639-644:
		//   params.chat_template_kwargs = { enable_thinking: !!options?.reasoningEffort, preserve_thinking: true };
		req.ChatTemplateKwargs = map[string]any{"enable_thinking": reasoning, "preserve_thinking": true}
	case "chat-template":
		// openai-completions.js:645-650: params.chat_template_kwargs = buildChatTemplateValues(model, options, compat.chatTemplateKwargs, thinkingBudget)
		if kwargs := resolveChatTemplateValues(model, opts, compat.ChatTemplateKwargs); kwargs != nil {
			req.ChatTemplateKwargs = kwargs
		}
	case "baseten":
		// openai-completions.js:651-665:
		//   basetenParams.chat_template_args = buildChatTemplateValues(model, options, compat.chatTemplateArgs, thinkingBudget)
		//   if (compat.supportsReasoningEffort) { ... basetenParams.reasoning_effort = effort; } -- note: baseten's
		//   effort mapping falls back to model.thinkingLevelMap?.off (not the raw level) when reasoningEffort is unset.
		if args := resolveChatTemplateValues(model, opts, compat.ChatTemplateArgs); args != nil {
			req.ChatTemplateArgs = args
		}
		if supportsEffort {
			level := opts.ThinkingLevel
			if !reasoning {
				level = provider.ThinkingOff
			}
			if effort, ok := mappedThinkingLevel(model, level); ok && (reasoning || effort != "") {
				req.ReasoningEffort = effort
			}
		}
	case "deepseek":
		// openai-completions.js:666-677:
		//   if (options?.reasoningEffort) params.thinking = { type: "enabled" };
		//   else if (model.thinkingLevelMap?.off !== null) params.thinking = { type: "disabled" };
		//   if (options?.reasoningEffort && compat.supportsReasoningEffort) params.reasoning_effort = mapped ?? level;
		if reasoning {
			req.Thinking, _ = json.Marshal(map[string]any{"type": "enabled"})
		} else if _, ok := mappedThinkingLevel(model, provider.ThinkingOff); ok {
			req.Thinking, _ = json.Marshal(map[string]any{"type": "disabled"})
		}
		if reasoning && supportsEffort {
			if effort, ok := mappedThinkingLevel(model, opts.ThinkingLevel); ok {
				req.ReasoningEffort = effort
			}
		}
	case "ant-ling":
		// openai-completions.js:690-695: only sent when reasoningEffort is set AND the mapped effort is non-null.
		if reasoning {
			if model.ThinkingLevelMap != nil {
				if mapped, present := model.ThinkingLevelMap[opts.ThinkingLevel]; present && mapped != nil {
					req.Reasoning, _ = json.Marshal(map[string]any{"effort": *mapped})
				}
			}
		}
	case "together":
		// openai-completions.js:696-703:
		//   togetherParams.reasoning = { enabled: !!options?.reasoningEffort };
		//   if (options?.reasoningEffort && compat.supportsReasoningEffort) togetherParams.reasoning_effort = mapped ?? level;
		req.Reasoning, _ = json.Marshal(map[string]any{"enabled": reasoning})
		if reasoning && supportsEffort {
			if effort, ok := mappedThinkingLevel(model, opts.ThinkingLevel); ok {
				req.ReasoningEffort = effort
			}
		}
	case "string-thinking":
		// openai-completions.js:704-711:
		//   if (options?.reasoningEffort) stringThinkingParams.thinking = mapped ?? level;
		//   else if (model.thinkingLevelMap?.off !== null) stringThinkingParams.thinking = model.thinkingLevelMap?.off ?? "none";
		if reasoning {
			if effort, ok := mappedThinkingLevel(model, opts.ThinkingLevel); ok {
				req.Thinking, _ = json.Marshal(effort)
			}
		} else if off, ok := mappedThinkingLevel(model, provider.ThinkingOff); ok {
			if off == "" {
				off = "none"
			}
			req.Thinking, _ = json.Marshal(off)
		}
	default:
		// openai-completions.js:712-717 (the plain "openai" format, also the
		// fallback for any unrecognized thinkingFormat string):
		//   else if (options?.reasoningEffort && ...) params.reasoning_effort = mapped ?? level;
		//   else if (!options?.reasoningEffort && ...) { if (typeof offValue === "string") params.reasoning_effort = offValue; }
		if !supportsEffort {
			return
		}
		if reasoning {
			if effort, ok := mappedThinkingLevel(model, opts.ThinkingLevel); ok {
				req.ReasoningEffort = effort
			}
			return
		}
		if model.ThinkingLevelMap != nil {
			if off, present := model.ThinkingLevelMap[provider.ThinkingOff]; present && off != nil {
				req.ReasoningEffort = *off
			}
		}
	}
}

// resolveChatTemplateValues ports resolveChatTemplateKwargValue
// (openai-completions.js:765-788) over a whole kwargs/args map: each
// {"$var": "thinking.enabled"} resolves to a bool, {"$var": "thinking.budget"}
// resolves to the thinking-token budget for the request (this harness has no
// options.thinkingBudgets, so it reuses budgetForThinkingLevel from
// anthropic_messages.go as its best-effort budget source -- a deviation from
// pi's clampThinkingBudgetToAnswerRoom, noted in the phase report), any other
// object value falls back to the mapped thinking level (pi's
// `model.thinkingLevelMap?.[level] ?? level`, omitted if unmapped-to-null),
// and non-object values pass through unchanged. A value with `omitWhenOff:
// true` is dropped entirely when reasoning is not requested. Returns nil if
// the resolved map ends up empty (matches pi returning undefined).
func resolveChatTemplateValues(model provider.Model, opts provider.StreamOptions, values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	reasoning := wantReasoning(opts)
	out := map[string]any{}
	for key, raw := range values {
		obj, isObj := raw.(map[string]any)
		if !isObj {
			out[key] = raw
			continue
		}
		if omit, _ := obj["omitWhenOff"].(bool); omit && !reasoning {
			continue
		}
		switch obj["$var"] {
		case "thinking.enabled":
			out[key] = reasoning
			continue
		case "thinking.budget":
			budget := 0
			if reasoning {
				budget = budgetForThinkingLevel(opts.ThinkingLevel)
			}
			if budget > 0 {
				out[key] = budget
			}
			continue
		}
		level := opts.ThinkingLevel
		if !reasoning {
			level = provider.ThinkingOff
		}
		if effort, ok := mappedThinkingLevel(model, level); ok && effort != "" {
			out[key] = effort
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func boolPtr(b bool) *bool { return &b }

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
			ReasoningText    string               `json:"reasoning_text"`
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

		// Some endpoints return reasoning in reasoning_content (llama.cpp), or
		// reasoning (other openai-compatible endpoints), or reasoning_text.
		// Use the first non-empty field per chunk to avoid duplication (e.g.
		// chutes.ai returns both reasoning_content and reasoning with the
		// same content) -- openai-completions.js:395-420.
		reasoning := delta.ReasoningContent
		if reasoning == "" {
			reasoning = delta.Reasoning
		}
		if reasoning == "" {
			reasoning = delta.ReasoningText
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
		// A read error before the terminal event (unexpected EOF from a
		// mid-stream disconnect, or another transport failure) is a
		// retryable transport failure, not a context cancellation -- unless
		// the context itself was canceled/timed out, in which case that
		// takes precedence and the stream is reported as aborted, not
		// retried.
		if ctx.Err() != nil {
			return errorOut(partial, events, true, streamErr)
		}
		return errorOut(partial, events, false, provider.StreamInterrupted{Cause: streamErr})
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
