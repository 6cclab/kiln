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

// Anthropic Messages API streaming client, ported from pi-ai's
// dist/api/anthropic-messages.js. Request construction (system prompt
// placement, tool schema mapping, tool_result/tool role mapping, image
// blocks, thinking, cache_control, auth) and response parsing (usage
// fields, stop reason mapping, responseId) follow that file; the
// Claude-Code stealth tool-renaming, adaptive/mid-conversation-effort and
// server-side-fallback paths (anthropic-messages.js lines ~700-920, used
// only by Anthropic's own managed-effort models) are out of scope for this
// phase and are noted as a deviation in the phase-2 report.

const anthropicVersion = "2023-06-01"

// Beta feature strings pi sends, verified against
// pi-ai/dist/api/anthropic-messages.js:104-109.
const (
	betaFineGrainedToolStreaming = "fine-grained-tool-streaming-2025-05-14"
	betaInterleavedThinking      = "interleaved-thinking-2025-05-14"
)

// AnthropicClient streams completions against one Anthropic-messages-shaped
// endpoint (model.BaseURL + "/v1/messages").
type AnthropicClient struct {
	HTTPClient *http.Client
}

func (c *AnthropicClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// --- wire request shapes, matching the faux server's decoder and pi's encoder ---

type anthropicContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	Source    *anthropicImage `json:"source,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   any             `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	CacheCtrl *cacheControl   `json:"cache_control,omitempty"`
}

type anthropicImage struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type cacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

type anthropicWireMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
	CacheCtrl   *cacheControl   `json:"cache_control,omitempty"`
}

type anthropicThinking struct {
	Type       string `json:"type"`
	BudgetToks int    `json:"budget_tokens,omitempty"`
}

type anthropicRequest struct {
	Model       string                  `json:"model"`
	System      []anthropicContentBlock `json:"system,omitempty"`
	Messages    []anthropicWireMessage  `json:"messages"`
	Tools       []anthropicTool         `json:"tools,omitempty"`
	MaxTokens   int                     `json:"max_tokens"`
	Stream      bool                    `json:"stream"`
	Thinking    *anthropicThinking      `json:"thinking,omitempty"`
	Temperature *float64                `json:"temperature,omitempty"`
}

// --- request construction ---

// budgetForThinkingLevel maps a ThinkingLevel to a token budget for
// budget-based (non-adaptive) thinking, matching pi's
// adjustMaxTokensForThinking defaults for models without a
// thinkingLevelMap override.
func budgetForThinkingLevel(level provider.ThinkingLevel) int {
	switch level {
	case provider.ThinkingMinimal:
		return 1024
	case provider.ThinkingLow:
		return 2048
	case provider.ThinkingMedium:
		return 8192
	case provider.ThinkingHigh, provider.ThinkingXHigh, provider.ThinkingMax:
		return 16384
	default:
		return 0
	}
}

func buildAnthropicRequest(model provider.Model, transcript []msg.Message, opts provider.StreamOptions, auth Auth) anthropicRequest {
	req := anthropicRequest{
		Model:     model.ID,
		MaxTokens: model.MaxTokens,
		Stream:    true,
	}
	if opts.MaxTokens > 0 {
		req.MaxTokens = opts.MaxTokens
	}

	compat := model.AnthropicMessagesCompat()
	retention := "short"
	var ttl string
	if retention == "long" && boolDefault(compat.SupportsLongCacheRetention, true) {
		ttl = "1h"
	}
	cc := &cacheControl{Type: "ephemeral", TTL: ttl}

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

	if auth.IsOAuth {
		req.System = append(req.System, anthropicContentBlock{
			Type:      "text",
			Text:      "You are Claude Code, Anthropic's official CLI for Claude.",
			CacheCtrl: cc,
		})
	}
	if systemText != "" {
		req.System = append(req.System, anthropicContentBlock{Type: "text", Text: systemText, CacheCtrl: cc})
	}

	for _, m := range transcript {
		switch t := m.(type) {
		case msg.SystemMessage:
			// folded into req.System above
		case msg.UserMessage:
			req.Messages = append(req.Messages, anthropicWireMessage{Role: "user", Content: convertBlocksToAnthropic(t.Content)})
		case msg.AssistantMessage:
			req.Messages = append(req.Messages, anthropicWireMessage{Role: "assistant", Content: convertBlocksToAnthropic(t.Content)})
		case msg.ToolResultMessage:
			// A text-only result stays a plain string (the common case and
			// the shape every recorded request expects); a result carrying
			// an image (the read tool on a PNG) must be sent as blocks or
			// the image is silently dropped and the model only ever sees the
			// text fallback.
			var content any = msg.TextOf(t.Content)
			if hasNonText(t.Content) {
				content = convertBlocksToAnthropic(t.Content)
			}
			block := anthropicContentBlock{
				Type:      "tool_result",
				ToolUseID: t.ToolCallID,
				Content:   content,
				IsError:   t.IsError,
			}
			req.Messages = append(req.Messages, anthropicWireMessage{Role: "user", Content: []anthropicContentBlock{block}})
		}
	}

	if len(opts.Tools) > 0 {
		strict := model.SupportsStrictMode()
		_ = strict
		for i, td := range opts.Tools {
			schema := td.Parameters
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			tool := anthropicTool{Name: td.Name, Description: td.Description, InputSchema: schema}
			if i == len(opts.Tools)-1 && boolDefault(compat.SupportsCacheControlOnTools, true) {
				tool.CacheCtrl = cc
			}
			req.Tools = append(req.Tools, tool)
		}
	}

	if model.Reasoning && opts.ThinkingLevel != "" && opts.ThinkingLevel != provider.ThinkingOff {
		budget := budgetForThinkingLevel(opts.ThinkingLevel)
		if model.ThinkingLevelMap != nil {
			if mapped, ok := model.ThinkingLevelMap[opts.ThinkingLevel]; ok && mapped != nil {
				// A provider/model-specific string override; still expressed
				// as a budget since this client only implements
				// budget-based (non-adaptive) thinking this phase.
				_ = mapped
			}
		}
		if budget > 0 {
			if budget > req.MaxTokens-1 {
				budget = req.MaxTokens - 1
			}
			if budget > 0 {
				req.Thinking = &anthropicThinking{Type: "enabled", BudgetToks: budget}
			}
		}
	} else if model.Reasoning && opts.ThinkingLevel == provider.ThinkingOff {
		req.Thinking = &anthropicThinking{Type: "disabled"}
	}

	// Temperature is incompatible with extended thinking.
	if opts.Temperature != nil && req.Thinking == nil && boolDefault(compat.SupportsTemperature, true) {
		req.Temperature = opts.Temperature
	}

	return req
}

func boolDefault(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}

func convertBlocksToAnthropic(blocks msg.Blocks) []anthropicContentBlock {
	out := make([]anthropicContentBlock, 0, len(blocks))
	for _, b := range blocks {
		switch c := b.(type) {
		case msg.TextContent:
			out = append(out, anthropicContentBlock{Type: "text", Text: c.Text})
		case msg.ImageContent:
			out = append(out, anthropicContentBlock{Type: "image", Source: &anthropicImage{Type: "base64", MediaType: c.MimeType, Data: c.Data}})
		case msg.ThinkingContent:
			// The API accepts a replayed thinking block only with the
			// signature it issued, in the `thinking` + `signature` fields.
			// An unsigned block (reasoning from another provider, replayed
			// after a /model switch) is dropped rather than sent: sending it
			// fails the whole request with "thinking.thinking: Field
			// required" (seen live, req_011CfQRRsevfwAHjMgHsbixh), and the
			// text is the model's own scratch work, not conversation state.
			if c.ThinkingSignature == "" {
				continue
			}
			out = append(out, anthropicContentBlock{Type: "thinking", Thinking: c.Thinking, Signature: c.ThinkingSignature})
		case msg.ToolCall:
			args, _ := json.Marshal(c.Arguments)
			out = append(out, anthropicContentBlock{Type: "tool_use", ID: c.ID, Name: c.Name, Input: args})
		}
	}
	return out
}

func anthropicHeaders(model provider.Model, auth Auth, hasTools bool) http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json")
	h.Set("anthropic-dangerous-direct-browser-access", "true")
	// Always sent: pi's Anthropic SDK client adds it on every request,
	// and the API rejects an OAuth request without it (400, "anthropic-
	// version: header is required", seen live).
	h.Set("anthropic-version", anthropicVersion)

	var betas []string
	if auth.IsOAuth {
		h.Set("Authorization", "Bearer "+auth.APIKey)
		h.Set("User-Agent", "claude-cli/2.1.280")
		h.Set("x-app", "cli")
		betas = append(betas, "claude-code-20250219", "oauth-2025-04-20")
	} else {
		h.Set("x-api-key", auth.APIKey)
	}
	compat := model.AnthropicMessagesCompat()
	if hasTools && !boolDefault(compat.SupportsEagerToolInputStreaming, true) {
		betas = append(betas, betaFineGrainedToolStreaming)
	}
	if model.Reasoning {
		betas = append(betas, betaInterleavedThinking)
	}
	if len(betas) > 0 {
		h.Set("anthropic-beta", strings.Join(betas, ","))
	}
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

type anthropicSSEMessageStart struct {
	Message struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

type anthropicSSEContentBlockStart struct {
	Index        int `json:"index"`
	ContentBlock struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		Thinking  string          `json:"thinking"`
		Signature string          `json:"signature"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
	} `json:"content_block"`
}

type anthropicSSEContentBlockDelta struct {
	Index int `json:"index"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
		Signature   string `json:"signature"`
	} `json:"delta"`
}

type anthropicSSEContentBlockStop struct {
	Index int `json:"index"`
}

type anthropicSSEMessageDelta struct {
	Delta struct {
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage struct {
		InputTokens              *int `json:"input_tokens"`
		OutputTokens             *int `json:"output_tokens"`
		CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// mapAnthropicStopReason mirrors pi's mapStopReason.
func mapAnthropicStopReason(reason string) (msg.StopReason, string) {
	switch reason {
	case "end_turn", "pause_turn", "stop_sequence":
		return msg.StopStop, ""
	case "max_tokens":
		return msg.StopLength, ""
	case "tool_use":
		return msg.StopToolUse, ""
	case "refusal":
		return msg.StopError, "The model refused to complete the request"
	case "":
		return msg.StopPending, ""
	default:
		return msg.StopError, fmt.Sprintf("unhandled stop reason: %s", reason)
	}
}

// Stream starts an Anthropic Messages completion. The returned channel is
// closed once the stream ends; wait blocks for that and returns the final
// message or the terminating error.
func (c *AnthropicClient) Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions, auth Auth) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
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

func (c *AnthropicClient) run(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions, auth Auth, events chan<- msg.StreamEvent) (*msg.AssistantMessage, error) {
	partial := &msg.AssistantMessage{
		API:        string(provider.ApiAnthropicMessages),
		Provider:   model.Provider,
		Model:      model.ID,
		Role:       msg.RoleAssistant,
		StopReason: msg.StopPending,
		Content:    msg.Blocks{},
	}

	wireReq := buildAnthropicRequest(model, transcript, opts, auth)
	body, err := json.Marshal(wireReq)
	if err != nil {
		return errorOut(partial, events, false, err)
	}

	url := strings.TrimRight(model.BaseURL, "/") + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return errorOut(partial, events, false, err)
	}
	httpReq.Header = anthropicHeaders(model, auth, len(wireReq.Tools) > 0)

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

	// blockKind tracks each content index's kind so deltas route correctly.
	type blockInfo struct {
		kind        string // "text" | "thinking" | "toolCall"
		partialJSON string
	}
	blocks := map[int]*blockInfo{}
	indexOf := map[int]int{} // sse index -> position in partial.Content

	r := bufio.NewReader(resp.Body)
	streamErr := scanSSE(r, func(ev sseEvent) {
		if ev.Data == "" {
			return
		}
		switch ev.Event {
		case "message_start":
			var m anthropicSSEMessageStart
			if json.Unmarshal([]byte(ev.Data), &m) == nil {
				partial.ResponseID = m.Message.ID
				partial.Usage.Input = m.Message.Usage.InputTokens
				partial.Usage.Output = m.Message.Usage.OutputTokens
				partial.Usage.CacheRead = m.Message.Usage.CacheReadInputTokens
				partial.Usage.CacheWrite = m.Message.Usage.CacheCreationInputTokens
				partial.Usage.TotalTokens = partial.Usage.Input + partial.Usage.Output + partial.Usage.CacheRead + partial.Usage.CacheWrite
				computeAnthropicCost(model, &partial.Usage)
			}
		case "content_block_start":
			var cb anthropicSSEContentBlockStart
			if json.Unmarshal([]byte(ev.Data), &cb) != nil {
				return
			}
			switch cb.ContentBlock.Type {
			case "text":
				partial.Content = append(partial.Content, msg.Text(cb.ContentBlock.Text))
				pos := len(partial.Content) - 1
				indexOf[cb.Index] = pos
				blocks[cb.Index] = &blockInfo{kind: "text"}
				events <- msg.StreamEvent{Type: msg.EventTextStart, ContentIndex: pos, Partial: partial}
			case "thinking", "redacted_thinking":
				partial.Content = append(partial.Content, msg.Thinking(cb.ContentBlock.Thinking))
				pos := len(partial.Content) - 1
				indexOf[cb.Index] = pos
				blocks[cb.Index] = &blockInfo{kind: "thinking"}
				events <- msg.StreamEvent{Type: msg.EventThinkingStart, ContentIndex: pos, Partial: partial}
			case "tool_use":
				var args map[string]any
				if len(cb.ContentBlock.Input) > 0 {
					_ = json.Unmarshal(cb.ContentBlock.Input, &args)
				}
				partial.Content = append(partial.Content, msg.NewToolCall(cb.ContentBlock.ID, cb.ContentBlock.Name, args))
				pos := len(partial.Content) - 1
				indexOf[cb.Index] = pos
				blocks[cb.Index] = &blockInfo{kind: "toolCall"}
				events <- msg.StreamEvent{Type: msg.EventToolCallStart, ContentIndex: pos, Partial: partial}
			}
		case "content_block_delta":
			var cd anthropicSSEContentBlockDelta
			if json.Unmarshal([]byte(ev.Data), &cd) != nil {
				return
			}
			bi, ok := blocks[cd.Index]
			if !ok {
				return
			}
			pos := indexOf[cd.Index]
			switch cd.Delta.Type {
			case "text_delta":
				tc := partial.Content[pos].(msg.TextContent)
				tc.Text += cd.Delta.Text
				partial.Content[pos] = tc
				events <- msg.StreamEvent{Type: msg.EventTextDelta, ContentIndex: pos, Delta: cd.Delta.Text, Partial: partial}
			case "thinking_delta":
				tc := partial.Content[pos].(msg.ThinkingContent)
				tc.Thinking += cd.Delta.Thinking
				partial.Content[pos] = tc
				events <- msg.StreamEvent{Type: msg.EventThinkingDelta, ContentIndex: pos, Delta: cd.Delta.Thinking, Partial: partial}
			case "input_json_delta":
				bi.partialJSON += cd.Delta.PartialJSON
				tc := partial.Content[pos].(msg.ToolCall)
				var args map[string]any
				if json.Unmarshal([]byte(bi.partialJSON), &args) == nil {
					tc.Arguments = args
				}
				partial.Content[pos] = tc
				events <- msg.StreamEvent{Type: msg.EventToolCallDelta, ContentIndex: pos, Delta: cd.Delta.PartialJSON, Partial: partial}
			case "signature_delta":
				tc := partial.Content[pos].(msg.ThinkingContent)
				tc.ThinkingSignature += cd.Delta.Signature
				partial.Content[pos] = tc
			}
		case "content_block_stop":
			var cs anthropicSSEContentBlockStop
			if json.Unmarshal([]byte(ev.Data), &cs) != nil {
				return
			}
			bi, ok := blocks[cs.Index]
			if !ok {
				return
			}
			pos := indexOf[cs.Index]
			switch bi.kind {
			case "text":
				tc := partial.Content[pos].(msg.TextContent)
				events <- msg.StreamEvent{Type: msg.EventTextEnd, ContentIndex: pos, Content: tc.Text, Partial: partial}
			case "thinking":
				tc := partial.Content[pos].(msg.ThinkingContent)
				events <- msg.StreamEvent{Type: msg.EventThinkingEnd, ContentIndex: pos, Content: tc.Thinking, Partial: partial}
			case "toolCall":
				tc := partial.Content[pos].(msg.ToolCall)
				// Validate the fully-accumulated argument JSON now that the
				// block is closed, rather than trusting whatever the last
				// successful per-delta parse left in tc.Arguments: each
				// input_json_delta above only updates Arguments when that
				// delta's accumulated prefix happens to parse, so a stream
				// that ends mid-object (e.g. a raw, unterminated
				// `{"path": `) silently leaves Arguments at its last-good
				// value (often {}) with no signal that the model's real
				// tool call was malformed. Re-parsing the whole
				// accumulated string here catches that.
				if bi.partialJSON != "" {
					var args map[string]any
					if err := json.Unmarshal([]byte(bi.partialJSON), &args); err != nil {
						tc.Arguments = map[string]any{}
						tc.InvalidArgs = bi.partialJSON
					} else {
						tc.Arguments = args
						tc.InvalidArgs = ""
					}
					partial.Content[pos] = tc
				}
				events <- msg.StreamEvent{Type: msg.EventToolCallEnd, ContentIndex: pos, ToolCall: &tc, Partial: partial}
			}
		case "message_delta":
			var md anthropicSSEMessageDelta
			if json.Unmarshal([]byte(ev.Data), &md) != nil {
				return
			}
			if md.Delta.StopReason != "" {
				partial.RawStopReason = md.Delta.StopReason
				reason, errMsg := mapAnthropicStopReason(md.Delta.StopReason)
				partial.StopReason = reason
				if errMsg != "" {
					partial.ErrorMessage = errMsg
				}
			}
			if md.Usage.InputTokens != nil {
				partial.Usage.Input = *md.Usage.InputTokens
			}
			if md.Usage.OutputTokens != nil {
				partial.Usage.Output = *md.Usage.OutputTokens
			}
			if md.Usage.CacheReadInputTokens != nil {
				partial.Usage.CacheRead = *md.Usage.CacheReadInputTokens
			}
			if md.Usage.CacheCreationInputTokens != nil {
				partial.Usage.CacheWrite = *md.Usage.CacheCreationInputTokens
			}
			partial.Usage.TotalTokens = partial.Usage.Input + partial.Usage.Output + partial.Usage.CacheRead + partial.Usage.CacheWrite
			computeAnthropicCost(model, &partial.Usage)
		}
	})
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

	if ctx.Err() != nil {
		return errorOut(partial, events, true, fmt.Errorf("request was aborted"))
	}
	if partial.StopReason == msg.StopPending {
		return errorOut(partial, events, false, fmt.Errorf("anthropic stream ended without a stop reason"))
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

func errorOut(partial *msg.AssistantMessage, events chan<- msg.StreamEvent, aborted bool, err error) (*msg.AssistantMessage, error) {
	if aborted {
		partial.StopReason = msg.StopAborted
	} else {
		partial.StopReason = msg.StopError
	}
	partial.ErrorMessage = err.Error()
	events <- msg.StreamEvent{Type: msg.EventError, Reason: partial.StopReason, Error: partial}
	return nil, err
}

// computeAnthropicCost fills usage.Cost from model.Cost, matching pi's
// calculateCost for the (non-tiered) common case.
func computeAnthropicCost(model provider.Model, u *msg.Usage) {
	rates := model.Cost.ModelCostRates
	u.Cost = msg.Cost{
		Input:      float64(u.Input) / 1_000_000 * rates.Input,
		Output:     float64(u.Output) / 1_000_000 * rates.Output,
		CacheRead:  float64(u.CacheRead) / 1_000_000 * rates.CacheRead,
		CacheWrite: float64(u.CacheWrite) / 1_000_000 * rates.CacheWrite,
	}
	u.Cost.Total = u.Cost.Input + u.Cost.Output + u.Cost.CacheRead + u.Cost.CacheWrite
}

// hasNonText reports whether blocks holds anything other than text.
func hasNonText(blocks msg.Blocks) bool {
	for _, b := range blocks {
		if _, ok := b.(msg.TextContent); !ok {
			return true
		}
	}
	return false
}
