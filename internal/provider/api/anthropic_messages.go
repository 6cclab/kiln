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
	"os"
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
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// Thinking is a pointer so a thinking block always carries the field,
	// even empty: models that omit their thinking text (Opus 4.8 by
	// default) return signed blocks with thinking "", and a replay without
	// the field fails with "thinking.thinking: Field required".
	Thinking  *string         `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	Source    *anthropicImage `json:"source,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   any             `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	CacheCtrl *cacheControl   `json:"cache_control,omitempty"`
	// Raw, when set, is a provider-native block (msg.ProviderBlock.Raw for
	// Provider=="anthropic": a server_tool_use or web_search_tool_result
	// block) replayed to the API verbatim. MarshalJSON below sends Raw
	// as-is instead of the struct's own fields when set.
	Raw json.RawMessage `json:"-"`
}

// MarshalJSON sends b.Raw verbatim when set (a replayed provider-native
// block), or the struct's own fields otherwise.
func (b anthropicContentBlock) MarshalJSON() ([]byte, error) {
	if b.Raw != nil {
		return b.Raw, nil
	}
	type alias anthropicContentBlock
	return json.Marshal(alias(b))
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
	// Raw, when set, is a provider.ToolDef.ServerTool declaration (e.g.
	// {"type":"web_search_20250305","name":"web_search","max_uses":5})
	// sent verbatim in place of the function-tool fields above.
	Raw json.RawMessage `json:"-"`
}

// MarshalJSON sends t.Raw verbatim when set (a server tool declaration),
// or the struct's own function-tool fields otherwise.
func (t anthropicTool) MarshalJSON() ([]byte, error) {
	if t.Raw != nil {
		return t.Raw, nil
	}
	type alias anthropicTool
	return json.Marshal(alias(t))
}

type anthropicThinking struct {
	Type       string `json:"type"`
	BudgetToks int    `json:"budget_tokens,omitempty"`
}

// anthropicOutputConfig carries the effort level adaptive thinking uses.
type anthropicOutputConfig struct {
	Effort string `json:"effort,omitempty"`
}

type anthropicRequest struct {
	Model        string                  `json:"model"`
	System       []anthropicContentBlock `json:"system,omitempty"`
	Messages     []anthropicWireMessage  `json:"messages"`
	Tools        []anthropicTool         `json:"tools,omitempty"`
	MaxTokens    int                     `json:"max_tokens"`
	Stream       bool                    `json:"stream"`
	Thinking     *anthropicThinking      `json:"thinking,omitempty"`
	OutputConfig *anthropicOutputConfig  `json:"output_config,omitempty"`
	Temperature  *float64                `json:"temperature,omitempty"`
}

// --- request construction ---

// adaptiveLevels is the order effort levels step up in.
var adaptiveLevels = []provider.ThinkingLevel{
	provider.ThinkingMinimal, provider.ThinkingLow, provider.ThinkingMedium,
	provider.ThinkingHigh, provider.ThinkingXHigh, provider.ThinkingMax,
}

// adaptiveEffort is the output_config.effort an adaptive-thinking model
// gets for level, from the catalog's thinkingLevelMap: a listed level uses
// its mapped name, an unlisted one its own name, and a level mapped to
// null (unsupported) steps up to the next supported one. ok is false only
// for "off" (or no level) on a model whose map does not rule "off" out:
// that model can still be sent thinking "disabled".
func adaptiveEffort(levels provider.ThinkingLevelMap, level provider.ThinkingLevel) (effort string, ok bool) {
	if level == provider.ThinkingOff {
		if mapped, listed := levels[provider.ThinkingOff]; !listed || mapped != nil {
			return "", false
		}
		level = provider.ThinkingMinimal
	}
	start := 0
	for i, l := range adaptiveLevels {
		if l == level {
			start = i
		}
	}
	for _, l := range adaptiveLevels[start:] {
		mapped, listed := levels[l]
		switch {
		case !listed && l != provider.ThinkingMinimal:
			return string(l), true
		case listed && mapped != nil:
			return *mapped, true
		}
	}
	return string(provider.ThinkingHigh), true
}

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

// cacheRetention is the main conversation's prompt-cache TTL bucket:
// "short" (5m, the default) or "long" (1h, billed at a higher cache-write
// rate in exchange for surviving longer gaps between requests).
//
// This is a kiln experiment switch (HARNESS_CACHE_RETENTION=long; see
// internal/cli/experiments.go), OFF (short) unless set. Claude Code has its
// own equivalent, CLAUDE_CODE_PROMPT_CACHE_TTL ("5m" or "1h"; any other
// value ignored) — docs/en/prompt-caching#choose-the-ttl-yourself,
// https://code.claude.com/docs/en/prompt-caching, fetched 2026-09-30 — so
// that is checked first to keep kiln behaving like Claude Code wherever it
// reads Claude Code's own config, with HARNESS_CACHE_RETENTION as a
// fallback for the benchmark rig.
func cacheRetention() string {
	switch os.Getenv("CLAUDE_CODE_PROMPT_CACHE_TTL") {
	case "1h":
		return "long"
	case "5m":
		return "short"
	}
	if os.Getenv("HARNESS_CACHE_RETENTION") == "long" {
		return "long"
	}
	return "short"
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
	retention := cacheRetention()
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
		// No breakpoint of its own: the system block's, right after it,
		// caches this prefix too, and the four-breakpoint limit is spent on
		// tools, system and the conversation (markConversationCache).
		req.System = append(req.System, anthropicContentBlock{
			Type: "text",
			Text: "You are Claude Code, Anthropic's official CLI for Claude.",
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

	markConversationCache(req.Messages, cc)

	if len(opts.Tools) > 0 {
		strict := model.SupportsStrictMode()
		_ = strict
		for i, td := range opts.Tools {
			var tool anthropicTool
			if len(td.ServerTool) > 0 {
				// A provider-native tool (e.g. web_search): its declaration
				// is sent verbatim, not built from Name/Parameters.
				tool = anthropicTool{Raw: td.ServerTool}
			} else {
				schema := td.Parameters
				if len(schema) == 0 {
					schema = json.RawMessage(`{"type":"object","properties":{}}`)
				}
				tool = anthropicTool{Name: td.Name, Description: td.Description, InputSchema: schema}
			}
			if i == len(opts.Tools)-1 && tool.Raw == nil && boolDefault(compat.SupportsCacheControlOnTools, true) {
				tool.CacheCtrl = cc
			}
			req.Tools = append(req.Tools, tool)
		}
	}

	switch {
	case !model.Reasoning:
	case boolDefault(compat.ForceAdaptiveThinking, false) && opts.ThinkingLevel == "":
		// No level asked for: adaptive thinking with no effort pinned, so
		// the model decides how much to think.
		req.Thinking = &anthropicThinking{Type: "adaptive"}
	case opts.ThinkingLevel == "":
		// Budget-thinking model, no level asked for: its own default.
	case boolDefault(compat.ForceAdaptiveThinking, false):
		// Adaptive-only models (Opus 4.8 and later) reject budget-based
		// thinking and, when the catalog maps "off" to null, "disabled"
		// too ("thinking.type.disabled is not supported for this model",
		// req_011CfW6VDzwAzRAgoSscp8x9). They take an effort level instead.
		if effort, ok := adaptiveEffort(model.ThinkingLevelMap, opts.ThinkingLevel); ok {
			req.Thinking = &anthropicThinking{Type: "adaptive"}
			req.OutputConfig = &anthropicOutputConfig{Effort: effort}
		} else {
			req.Thinking = &anthropicThinking{Type: "disabled"}
		}
	case opts.ThinkingLevel != "" && opts.ThinkingLevel != provider.ThinkingOff:
		budget := budgetForThinkingLevel(opts.ThinkingLevel)
		if budget > req.MaxTokens-1 {
			budget = req.MaxTokens - 1
		}
		if budget > 0 {
			req.Thinking = &anthropicThinking{Type: "enabled", BudgetToks: budget}
		}
	case opts.ThinkingLevel == provider.ThinkingOff:
		req.Thinking = &anthropicThinking{Type: "disabled"}
	}

	// Temperature is incompatible with extended thinking.
	if opts.Temperature != nil && req.Thinking == nil && boolDefault(compat.SupportsTemperature, true) {
		req.Temperature = opts.Temperature
	}

	applyRequestedCacheBreaks(&req, cc)
	return req
}

// markConversationCache puts a cache breakpoint on the last block of the
// last two user-side messages (prompts and tool results), so each request
// reads the conversation so far from the prompt cache instead of paying for
// it again. Without it only tools and system were cached, and a 45-request
// session paid full input price for 1.18M tokens of its own history. The
// newest breakpoint caches the prefix the next request extends; the one
// before it is the previous request's tail, which keeps the next request a
// cache hit even when a turn adds more blocks than the API's 20-block
// lookback (many parallel tool calls). With tools and system that is the
// API's limit of four breakpoints.
func markConversationCache(messages []anthropicWireMessage, cc *cacheControl) {
	marked := 0
	for i := len(messages) - 1; i >= 0 && marked < 2; i-- {
		if messages[i].Role != "user" {
			continue
		}
		blocks, ok := messages[i].Content.([]anthropicContentBlock)
		if !ok {
			continue
		}
		for j := len(blocks) - 1; j >= 0; j-- {
			if blocks[j].Type == "thinking" || blocks[j].Type == "redacted_thinking" {
				continue // not a cacheable block type
			}
			blocks[j].CacheCtrl = cc
			marked++
			break
		}
	}
}

// requestedCacheBreak marks a block whose msg.TextContent asked for a
// breakpoint (CacheBreak) until applyRequestedCacheBreaks resolves it.
var requestedCacheBreak = &cacheControl{Type: "ephemeral"}

// maxCacheBreakpoints is the API's limit on cache_control blocks.
const maxCacheBreakpoints = 4

// applyRequestedCacheBreaks turns the breakpoints blocks asked for into
// real ones while the request stays within the API's four, newest first,
// after the ones kiln always places (tools, system, the conversation's
// tail). A breakpoint inside a request's stable prefix lets the next
// request, whose prefix extends it, read it from the cache: the API looks
// for earlier cache entries at block boundaries up to 20 blocks back.
func applyRequestedCacheBreaks(req *anthropicRequest, cc *cacheControl) {
	used := 0
	for _, b := range req.System {
		if b.CacheCtrl != nil {
			used++
		}
	}
	for _, t := range req.Tools {
		if t.CacheCtrl != nil {
			used++
		}
	}
	for _, m := range req.Messages {
		if blocks, ok := m.Content.([]anthropicContentBlock); ok {
			for _, b := range blocks {
				if b.CacheCtrl != nil && b.CacheCtrl != requestedCacheBreak {
					used++
				}
			}
		}
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		blocks, ok := req.Messages[i].Content.([]anthropicContentBlock)
		if !ok {
			continue
		}
		for j := len(blocks) - 1; j >= 0; j-- {
			if blocks[j].CacheCtrl != requestedCacheBreak {
				continue
			}
			if used < maxCacheBreakpoints {
				blocks[j].CacheCtrl = cc
				used++
			} else {
				blocks[j].CacheCtrl = nil
			}
		}
	}
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
			block := anthropicContentBlock{Type: "text", Text: c.Text}
			if c.CacheBreak {
				block.CacheCtrl = requestedCacheBreak
			}
			out = append(out, block)
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
			thinking := c.Thinking
			out = append(out, anthropicContentBlock{Type: "thinking", Thinking: &thinking, Signature: c.ThinkingSignature})
		case msg.ToolCall:
			args, _ := json.Marshal(c.Arguments)
			out = append(out, anthropicContentBlock{Type: "tool_use", ID: c.ID, Name: c.Name, Input: args})
		case msg.ProviderBlock:
			// Replayed verbatim only for the provider that sent it (a
			// server_tool_use/web_search_tool_result pair recorded while
			// running against Anthropic). A ProviderBlock from any other
			// provider is silently dropped, same as every other provider's
			// own convertBlocksTo* leaving ProviderBlock unmatched in their
			// type switches.
			if c.Provider == "anthropic" {
				out = append(out, anthropicContentBlock{Raw: c.Raw})
			}
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
		// ToolUseID/Content are set on a web_search_tool_result block
		// (server_tool_use reuses ID/Name/Input above, same as tool_use).
		ToolUseID string          `json:"tool_use_id"`
		Content   json.RawMessage `json:"content"`
	} `json:"content_block"`
}

// anthropicServerToolUseWire is the JSON shape a server_tool_use
// ProviderBlock is stored/replayed as.
type anthropicServerToolUseWire struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// anthropicWebSearchResultWire is the JSON shape a web_search_tool_result
// ProviderBlock is stored/replayed as.
type anthropicWebSearchResultWire struct {
	Type      string          `json:"type"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
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
		ServerToolUse            *struct {
			WebSearchRequests int `json:"web_search_requests"`
		} `json:"server_tool_use"`
	} `json:"usage"`
}

// mapAnthropicStopReason mirrors pi's mapStopReason. pause_turn is its own
// reason, not folded into "stop": it means a long server-tool turn was cut
// for interim delivery, and the caller (internal/harness/turn.go's drive())
// must re-request rather than finish the operation.
func mapAnthropicStopReason(reason string) (msg.StopReason, string) {
	switch reason {
	case "end_turn", "stop_sequence":
		return msg.StopStop, ""
	case "pause_turn":
		return msg.StopPause, ""
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

	url := strings.TrimRight(anthropicBaseURL(model), "/") + "/v1/messages"
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
		kind         string // "text" | "thinking" | "toolCall" | "serverToolUse" | "providerBlockDone"
		partialJSON  string
		providerID   string
		providerName string
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
				// Cited text arrives as consecutive text blocks split at each
				// citation; kept as separate blocks they read as broken lines
				// (TextOf joins blocks with newlines). A text block right after
				// another continues it.
				if n := len(partial.Content); n > 0 {
					if prev, ok := partial.Content[n-1].(msg.TextContent); ok {
						prev.Text += cb.ContentBlock.Text
						partial.Content[n-1] = prev
						indexOf[cb.Index] = n - 1
						blocks[cb.Index] = &blockInfo{kind: "text"}
						break
					}
				}
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
			case "server_tool_use":
				// A provider-executed tool call (e.g. web_search): its
				// input streams in via input_json_delta exactly like a
				// tool_use block, but it is carried as a ProviderBlock, not
				// a msg.ToolCall, so the harness turn loop never treats it
				// as a client-side call to execute.
				initial, _ := json.Marshal(anthropicServerToolUseWire{
					Type: "server_tool_use", ID: cb.ContentBlock.ID, Name: cb.ContentBlock.Name, Input: json.RawMessage("{}"),
				})
				partial.Content = append(partial.Content, msg.ProviderBlock{Provider: "anthropic", Type: "providerBlock", Raw: initial})
				pos := len(partial.Content) - 1
				indexOf[cb.Index] = pos
				blocks[cb.Index] = &blockInfo{kind: "serverToolUse", providerID: cb.ContentBlock.ID, providerName: cb.ContentBlock.Name}
				events <- msg.StreamEvent{Type: msg.EventProviderBlockStart, ContentIndex: pos, Partial: partial}
			case "web_search_tool_result":
				// Complete as soon as it starts -- no deltas follow.
				raw, _ := json.Marshal(anthropicWebSearchResultWire{
					Type: "web_search_tool_result", ToolUseID: cb.ContentBlock.ToolUseID, Content: cb.ContentBlock.Content,
				})
				partial.Content = append(partial.Content, msg.ProviderBlock{Provider: "anthropic", Type: "providerBlock", Raw: raw})
				pos := len(partial.Content) - 1
				indexOf[cb.Index] = pos
				blocks[cb.Index] = &blockInfo{kind: "providerBlockDone"}
				events <- msg.StreamEvent{Type: msg.EventProviderBlockEnd, ContentIndex: pos, Content: string(raw), Partial: partial}
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
				if bi.kind == "serverToolUse" {
					// The input accumulates as raw JSON text; rebuilt into
					// the ProviderBlock's Raw at content_block_stop below,
					// once the whole object is known to be well-formed.
					events <- msg.StreamEvent{Type: msg.EventProviderBlockStart, ContentIndex: pos, Delta: cd.Delta.PartialJSON, Partial: partial}
					return
				}
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
			case "serverToolUse":
				input := json.RawMessage("{}")
				if bi.partialJSON != "" {
					var probe map[string]any
					if json.Unmarshal([]byte(bi.partialJSON), &probe) == nil {
						input = json.RawMessage(bi.partialJSON)
					}
				}
				raw, _ := json.Marshal(anthropicServerToolUseWire{
					Type: "server_tool_use", ID: bi.providerID, Name: bi.providerName, Input: input,
				})
				pb := msg.ProviderBlock{Provider: "anthropic", Type: "providerBlock", Raw: raw}
				partial.Content[pos] = pb
				events <- msg.StreamEvent{Type: msg.EventProviderBlockEnd, ContentIndex: pos, Content: string(raw), Partial: partial}
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
			if md.Usage.ServerToolUse != nil {
				partial.Usage.ServerToolUse = &msg.ServerToolUse{WebSearchRequests: md.Usage.ServerToolUse.WebSearchRequests}
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

// anthropicBaseURL is the endpoint for model: ANTHROPIC_BASE_URL when it is
// set and the model is Anthropic's own (as Claude Code honours it, for
// gateways and proxies), else the catalog's base URL. Other providers that
// speak the Messages API (MiniMax, Kimi, ...) keep their own endpoints.
func anthropicBaseURL(model provider.Model) string {
	if v := os.Getenv("ANTHROPIC_BASE_URL"); v != "" && model.Provider == "anthropic" {
		return v
	}
	return model.BaseURL
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
	if u.ServerToolUse != nil {
		// $10 per 1000 web searches, per Anthropic's published pricing.
		u.Cost.Search = float64(u.ServerToolUse.WebSearchRequests) / 1000 * 10
	}
	u.Cost.Total = u.Cost.Input + u.Cost.Output + u.Cost.CacheRead + u.Cost.CacheWrite + u.Cost.Search
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
