package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/andrepato/harness/internal/crash"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// Mistral "conversations" API streaming client, ported from pi-ai's
// dist/api/mistral-conversations.js.
//
// Surprise worth flagging up front: despite the file name and the
// ApiMistralConversations ("mistral-conversations") constant, this client
// does NOT speak Mistral's separate Conversations API. Its own doc comment
// says:
//
//	/** Stream responses from the native Mistral Chat Completions endpoint. */
//
// and it POSTs to `<baseUrl>/v1/chat/completions` with an
// OpenAI-chat-completions-shaped wire format (choices[].delta.content,
// choices[].delta.tool_calls, finish_reason, usage.prompt_tokens /
// completion_tokens). We keep the provider.ApiMistralConversations constant
// name (that's what types.go already defines and what the model catalog
// refers to) even though the wire protocol underneath is chat-completions
// shaped, not a "conversations" shape.
//
// Deviations from pi-ai, all noted inline near the code they affect:
//   - Framing: pi's readMistralEvents/parseMistralEvent split events on any
//     of several blank-line-like byte sequences (mixed line endings). We
//     reuse this package's scanSSE (sse.go), which only understands bare
//     "\n\n" / "\r\n\r\n" blank-line framing with "data:"/"event:" prefixes.
//     Real Mistral responses use plain SSE framing in practice; this is a
//     low-risk simplification.
//   - No toolChoice, no sessionId/promptCacheKey/x-affinity (no session
//     concept in this harness phase).
//   - No mid-conversation system message folding
//     (MistralConversationsCompat.SupportsMidConvoSystemMessages is decoded
//     but unused, same as the sibling clients).
//   - shortHash: pi's own internal short-hash utility is unavailable here;
//     we use sha256 hex truncated to 9 chars. Exact byte-for-byte parity
//     with pi's hash is not required, only that it derives a stable,
//     9-alphanumeric-char id and the attempt-increment retry loop still
//     works the same way.
//   - User-Agent is a literal string, not pi's getPiUserAgent().
//   - resolveJsonSchemaStrictSampling (constrained-sampling machinery) is
//     out of scope; we use model.SupportsStrictMode() directly.

// MistralConversationsClient streams completions against one Mistral
// chat-completions-shaped endpoint (model.BaseURL + "/v1/chat/completions").
type MistralConversationsClient struct {
	HTTPClient *http.Client
}

func (c *MistralConversationsClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// --- tool-call id normalization (pi's createMistralToolCallIdNormalizer /
// deriveMistralToolCallId, mistral-conversations.js:103-132) ---

const mistralToolCallIDLength = 9

// mistralShortHash is pi's shortHash, approximated. It is a package-level
// var (not a plain function) so tests can override it to exercise the
// attempt-increment collision-retry path deterministically, without relying
// on an actual sha256 collision.
var mistralShortHash = func(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// mistralAlnumOnly strips every byte that is not [a-zA-Z0-9], matching pi's
// `id.replace(/[^a-zA-Z0-9]/g, "")`.
func mistralAlnumOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// deriveMistralToolCallID mirrors pi's deriveMistralToolCallId exactly in
// shape: attempt 0 passes an already-conforming id through unchanged;
// otherwise it hash-derives a 9-alphanumeric-char id, salting the seed with
// ":<attempt>" on retries.
func deriveMistralToolCallID(id string, attempt int) string {
	normalized := mistralAlnumOnly(id)
	if attempt == 0 && len(normalized) == mistralToolCallIDLength {
		return normalized
	}
	seedBase := normalized
	if seedBase == "" {
		seedBase = id
	}
	seed := seedBase
	if attempt != 0 {
		seed = fmt.Sprintf("%s:%d", seedBase, attempt)
	}
	hashed := mistralAlnumOnly(mistralShortHash(seed))
	for len(hashed) < mistralToolCallIDLength {
		hashed += "0" // pad; sha256 hex is 64 chars, this should never trigger
	}
	return hashed[:mistralToolCallIDLength]
}

// mistralToolCallIDNormalizer mirrors pi's
// createMistralToolCallIdNormalizer: a per-request/per-stream closure that
// remaps arbitrary tool-call ids to Mistral's 9-alphanumeric-char format,
// retrying on collision against ids already handed out this request.
type mistralToolCallIDNormalizer struct {
	idMap      map[string]string
	reverseMap map[string]string
}

func newMistralToolCallIDNormalizer() *mistralToolCallIDNormalizer {
	return &mistralToolCallIDNormalizer{idMap: map[string]string{}, reverseMap: map[string]string{}}
}

func (n *mistralToolCallIDNormalizer) normalize(id string) string {
	if existing, ok := n.idMap[id]; ok {
		return existing
	}
	attempt := 0
	for {
		candidate := deriveMistralToolCallID(id, attempt)
		owner, exists := n.reverseMap[candidate]
		if !exists || owner == id {
			n.idMap[id] = candidate
			n.reverseMap[candidate] = id
			return candidate
		}
		attempt++
	}
}

// --- wire request shapes (buildChatPayload/toMistralWirePayload/toChatMessages) ---

// mistralWireThinkingPart is the nested array-of-parts shape pi's wire
// format uses for a thinking content part: {type:"thinking", thinking:[{type:"text", text}]}.
type mistralWireThinkingPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// mistralWireContentPart is one part of a message's `content` array.
// ImageURL is a bare string, NOT an {url:"..."} object like OpenAI's
// openAIImageURL in openai_completions.go -- confirmed by reading
// toMistralWireContentChunk / the image_url content-part construction in
// mistral-conversations.js:618 (`{ type: "image_url", imageUrl: ... }`,
// where imageUrl is later wire-remapped straight to image_url with no
// nested object).
type mistralWireContentPart struct {
	Type     string                    `json:"type"`
	Text     string                    `json:"text,omitempty"`
	Thinking []mistralWireThinkingPart `json:"thinking,omitempty"`
	ImageURL string                    `json:"image_url,omitempty"`
}

// mistralWireToolCall is one assistant-message tool call on the request
// side. Index is hardcoded to 0 for every tool call regardless of position,
// matching mistral-conversations.js:652 (`index: 0`) literally -- odd, but
// that's what pi's source does.
type mistralWireToolCall struct {
	ID       string                  `json:"id"`
	Type     string                  `json:"type"`
	Function mistralWireFunctionCall `json:"function"`
	Index    int                     `json:"index"`
}

type mistralWireFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type mistralWireMessage struct {
	Role       string                `json:"role"`
	Content    any                   `json:"content,omitempty"`
	ToolCalls  []mistralWireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string                `json:"tool_call_id,omitempty"`
	Name       string                `json:"name,omitempty"`
}

type mistralWireFunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      bool            `json:"strict"`
}

type mistralWireTool struct {
	Type     string                 `json:"type"`
	Function mistralWireFunctionDef `json:"function"`
}

type mistralRequest struct {
	Model           string               `json:"model"`
	Stream          bool                 `json:"stream"`
	Messages        []mistralWireMessage `json:"messages"`
	Tools           []mistralWireTool    `json:"tools,omitempty"`
	Temperature     *float64             `json:"temperature,omitempty"`
	MaxTokens       int                  `json:"max_tokens,omitempty"`
	ReasoningEffort string               `json:"reasoning_effort,omitempty"`
	PromptMode      string               `json:"prompt_mode,omitempty"`
}

// usesReasoningEffort mirrors mistral-conversations.js:708-713 exactly.
func usesReasoningEffort(modelID string) bool {
	return modelID == "mistral-small-2603" ||
		modelID == "mistral-small-latest" ||
		strings.HasPrefix(modelID, "mistral-medium-") ||
		modelID == "zai-glm-5-2"
}

// usesPromptModeReasoning mirrors mistral-conversations.js:714-716.
func usesPromptModeReasoning(model provider.Model) bool {
	return model.Reasoning && !usesReasoningEffort(model.ID)
}

func mistralContainsInput(inputs []string, want string) bool {
	for _, v := range inputs {
		if v == want {
			return true
		}
	}
	return false
}

// buildMistralToolResultText mirrors pi's buildToolResultText
// (mistral-conversations.js:691-707) exactly, all 6 branches.
func buildMistralToolResultText(text string, hasImages, supportsImages, isError bool) string {
	trimmed := strings.TrimSpace(text)
	errorPrefix := ""
	if isError {
		errorPrefix = "[tool error] "
	}
	if trimmed != "" {
		imageSuffix := ""
		if hasImages && !supportsImages {
			imageSuffix = "\n[tool image omitted: model does not support images]"
		}
		return errorPrefix + trimmed + imageSuffix
	}
	if hasImages {
		if supportsImages {
			if isError {
				return "[tool error] (see attached image)"
			}
			return "(see attached image)"
		}
		if isError {
			return "[tool error] (image omitted: model does not support images)"
		}
		return "(image omitted: model does not support images)"
	}
	if isError {
		return "[tool error] (no tool output)"
	}
	return "(no tool output)"
}

// buildMistralRequest mirrors buildChatPayload + toChatMessages. normalizeID
// is one mistralToolCallIDNormalizer.normalize closure shared by every
// assistant tool call and tool result in this request, so a call and its
// matching result always agree on the normalized id.
func buildMistralRequest(model provider.Model, transcript []msg.Message, opts provider.StreamOptions, normalizeID func(string) string) mistralRequest {
	supportsImages := mistralContainsInput(model.Input, "image")

	req := mistralRequest{Model: model.ID, Stream: true}

	// System message folding: this harness has no multi-turn transcript
	// folding for mid-conversation system messages (see
	// MistralConversationsCompat.SupportsMidConvoSystemMessages), so, like
	// the sibling clients, we fold to the first SystemMessage / SystemPrompt
	// only -- deviation from pi's getSystemMessageText/renderSystemMessageUpdate
	// per-later-message handling.
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
		req.Messages = append(req.Messages, mistralWireMessage{Role: "system", Content: systemText})
	}

	for _, m := range transcript {
		switch t := m.(type) {
		case msg.SystemMessage:
			// folded above
		case msg.UserMessage:
			req.Messages = append(req.Messages, buildMistralUserMessage(t, supportsImages)...)
		case msg.AssistantMessage:
			if wm, ok := buildMistralAssistantMessage(t, normalizeID); ok {
				req.Messages = append(req.Messages, wm)
			}
		case msg.ToolResultMessage:
			req.Messages = append(req.Messages, buildMistralToolResultMessage(t, supportsImages, normalizeID))
		}
	}

	if len(opts.Tools) > 0 {
		req.Tools = convertToMistralTools(opts.Tools, model)
	}

	// pi only sets maxTokens when options.maxTokens is explicitly given --
	// unlike anthropic_messages.go/openai_completions.go, there is no
	// fallback to model.MaxTokens here (mistral-conversations.js:367-368).
	if opts.MaxTokens > 0 {
		req.MaxTokens = opts.MaxTokens
	}
	if opts.Temperature != nil {
		req.Temperature = opts.Temperature
	}

	if model.Reasoning && opts.ThinkingLevel != "" && opts.ThinkingLevel != provider.ThinkingOff {
		if usesReasoningEffort(model.ID) {
			// pi's mapReasoningEffort fallback is the string literal "high",
			// NOT the raw thinking level string -- unlike every other client
			// in this package (which falls back to the raw level string).
			// mistral-conversations.js:717-719:
			//   function mapReasoningEffort(model, level) {
			//       return model.thinkingLevelMap?.[level] ?? "high";
			//   }
			effort := "high"
			if model.ThinkingLevelMap != nil {
				if mapped, ok := model.ThinkingLevelMap[opts.ThinkingLevel]; ok && mapped != nil {
					effort = *mapped
				}
			}
			req.ReasoningEffort = effort
		} else if usesPromptModeReasoning(model) {
			req.PromptMode = "reasoning"
		}
	}

	return req
}

func buildMistralUserMessage(t msg.UserMessage, supportsImages bool) []mistralWireMessage {
	// A single text block collapses to a bare string, matching pi's
	// `typeof msg.content === "string"` fast path (this harness's Blocks
	// representation loses the distinction between "was a string" and "was
	// a one-item text array", so we treat them the same -- they're
	// semantically identical here).
	if len(t.Content) == 1 {
		if tc, ok := t.Content[0].(msg.TextContent); ok {
			return []mistralWireMessage{{Role: "user", Content: tc.Text}}
		}
	}

	hadImages := false
	var parts []mistralWireContentPart
	for _, b := range t.Content {
		switch c := b.(type) {
		case msg.TextContent:
			parts = append(parts, mistralWireContentPart{Type: "text", Text: c.Text})
		case msg.ImageContent:
			hadImages = true
			if supportsImages {
				parts = append(parts, mistralWireContentPart{Type: "image_url", ImageURL: "data:" + c.MimeType + ";base64," + c.Data})
			}
		}
	}
	if len(parts) > 0 {
		return []mistralWireMessage{{Role: "user", Content: parts}}
	}
	if hadImages && !supportsImages {
		return []mistralWireMessage{{Role: "user", Content: "(image omitted: model does not support images)"}}
	}
	return nil
}

func buildMistralAssistantMessage(t msg.AssistantMessage, normalizeID func(string) string) (mistralWireMessage, bool) {
	var contentParts []mistralWireContentPart
	var toolCalls []mistralWireToolCall
	for _, b := range t.Content {
		switch c := b.(type) {
		case msg.TextContent:
			if strings.TrimSpace(c.Text) != "" {
				contentParts = append(contentParts, mistralWireContentPart{Type: "text", Text: c.Text})
			}
		case msg.ThinkingContent:
			if strings.TrimSpace(c.Thinking) != "" {
				contentParts = append(contentParts, mistralWireContentPart{
					Type:     "thinking",
					Thinking: []mistralWireThinkingPart{{Type: "text", Text: c.Thinking}},
				})
			}
		case msg.ToolCall:
			args, _ := json.Marshal(c.Arguments)
			toolCalls = append(toolCalls, mistralWireToolCall{
				ID:       normalizeID(c.ID),
				Type:     "function",
				Function: mistralWireFunctionCall{Name: c.Name, Arguments: string(args)},
				Index:    0, // pi hardcodes index:0 for every tool call -- see mistralWireToolCall doc
			})
		}
	}
	if len(contentParts) == 0 && len(toolCalls) == 0 {
		return mistralWireMessage{}, false
	}
	wm := mistralWireMessage{Role: "assistant"}
	if len(contentParts) > 0 {
		wm.Content = contentParts
	}
	if len(toolCalls) > 0 {
		wm.ToolCalls = toolCalls
	}
	return wm, true
}

func buildMistralToolResultMessage(t msg.ToolResultMessage, supportsImages bool, normalizeID func(string) string) mistralWireMessage {
	var textParts []string
	hasImages := false
	for _, b := range t.Content {
		switch c := b.(type) {
		case msg.TextContent:
			textParts = append(textParts, c.Text)
		case msg.ImageContent:
			hasImages = true
		}
	}
	textResult := strings.Join(textParts, "\n")
	toolText := buildMistralToolResultText(textResult, hasImages, supportsImages, t.IsError)

	content := []mistralWireContentPart{{Type: "text", Text: toolText}}
	if supportsImages {
		for _, b := range t.Content {
			if img, ok := b.(msg.ImageContent); ok {
				content = append(content, mistralWireContentPart{Type: "image_url", ImageURL: "data:" + img.MimeType + ";base64," + img.Data})
			}
		}
	}

	return mistralWireMessage{
		Role:       "tool",
		ToolCallID: normalizeID(t.ToolCallID),
		Name:       t.ToolName,
		Content:    content,
	}
}

// convertToMistralTools mirrors toFunctionTools (mistral-conversations.js:571-583).
// resolveJsonSchemaStrictSampling (constrained-sampling infra) is out of
// scope; model.SupportsStrictMode() is used directly as the boolean.
func convertToMistralTools(tools []provider.ToolDef, model provider.Model) []mistralWireTool {
	strict := model.SupportsStrictMode()
	out := make([]mistralWireTool, 0, len(tools))
	for _, td := range tools {
		if len(td.ServerTool) > 0 {
			continue
		}
		schema := td.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, mistralWireTool{
			Type: "function",
			Function: mistralWireFunctionDef{
				Name:        td.Name,
				Description: td.Description,
				Parameters:  schema,
				Strict:      strict,
			},
		})
	}
	return out
}

// mistralHeaders mirrors buildMistralHeaders (mistral-conversations.js:194-208),
// minus the x-affinity/sessionId prompt-caching header (no session concept
// this phase) and pi's getPiUserAgent() (a literal instead).
func mistralHeaders(model provider.Model, auth Auth) http.Header {
	h := http.Header{}
	h.Set("User-Agent", "harness-go/mistral-conversations")
	h.Set("Accept", "text/event-stream")
	h.Set("Authorization", "Bearer "+auth.APIKey)
	h.Set("Content-Type", "application/json")
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

// --- response wire shapes (consumeChatStream) ---

// mistralContentItem is one normalized item out of a delta.content payload,
// after collapsing pi's string-or-array-of-typed-parts shape. Kind is
// "text" or "thinking"; for "thinking" Text is already the join of every
// thinking.thinking[].text part (mistral-conversations.js:470-473).
type mistralContentItem struct {
	Kind string
	Text string
}

// mistralDeltaContent decodes delta.content, which pi's wire format allows
// to be a bare string, an array of typed parts, or null/absent.
type mistralDeltaContent struct {
	Items  []mistralContentItem
	IsNull bool
}

func (d *mistralDeltaContent) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		d.IsNull = true
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		d.Items = []mistralContentItem{{Kind: "text", Text: s}}
		return nil
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(data, &raws); err != nil {
		return err
	}
	for _, raw := range raws {
		var probe struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			continue
		}
		switch probe.Type {
		case "text":
			d.Items = append(d.Items, mistralContentItem{Kind: "text", Text: probe.Text})
		case "thinking":
			var th struct {
				Thinking []struct {
					Text string `json:"text"`
				} `json:"thinking"`
			}
			if err := json.Unmarshal(raw, &th); err != nil {
				continue
			}
			var sb strings.Builder
			for _, p := range th.Thinking {
				sb.WriteString(p.Text)
			}
			d.Items = append(d.Items, mistralContentItem{Kind: "thinking", Text: sb.String()})
		}
	}
	return nil
}

// mistralArgsDelta decodes one tool-call delta's function.arguments, which
// may arrive as a JSON string (the normal case) or, defensively, as a raw
// JSON object -- mistral-conversations.js:540-542:
//
//	const argsDelta = typeof toolCall.function.arguments === "string"
//	    ? toolCall.function.arguments
//	    : JSON.stringify(toolCall.function.arguments || {});
type mistralArgsDelta string

func (a *mistralArgsDelta) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*a = "{}"
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*a = mistralArgsDelta(s)
		return nil
	}
	*a = mistralArgsDelta(trimmed)
	return nil
}

type mistralWireToolCallDelta struct {
	Index    *int   `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string           `json:"name"`
		Arguments mistralArgsDelta `json:"arguments"`
	} `json:"function"`
}

type mistralDelta struct {
	Content   mistralDeltaContent        `json:"content"`
	ToolCalls []mistralWireToolCallDelta `json:"tool_calls"`
}

// mistralCachedTokensDetail covers both spellings of a nested cached-token
// count pi defensively checks (mistral-conversations.js:382-393).
type mistralCachedTokensDetail struct {
	CachedTokensCamel *int `json:"cachedTokens"`
	CachedTokensSnake *int `json:"cached_tokens"`
}

func (d *mistralCachedTokensDetail) cached() (int, bool) {
	if d == nil {
		return 0, false
	}
	if d.CachedTokensCamel != nil {
		return *d.CachedTokensCamel, true
	}
	if d.CachedTokensSnake != nil {
		return *d.CachedTokensSnake, true
	}
	return 0, false
}

type mistralUsageWire struct {
	PromptTokens         int                        `json:"prompt_tokens"`
	CompletionTokens     int                        `json:"completion_tokens"`
	TotalTokens          int                        `json:"total_tokens"`
	PromptTokensDetails  *mistralCachedTokensDetail `json:"promptTokensDetails"`
	PromptTokensDetails_ *mistralCachedTokensDetail `json:"prompt_tokens_details"`
	PromptTokenDetails   *mistralCachedTokensDetail `json:"promptTokenDetails"`
	PromptTokenDetails_  *mistralCachedTokensDetail `json:"prompt_token_details"`
	NumCachedTokens      *int                       `json:"numCachedTokens"`
	NumCachedTokens_     *int                       `json:"num_cached_tokens"`
}

// getMistralCachedPromptTokens mirrors pi's getMistralCachedPromptTokens
// (mistral-conversations.js:382-393): a defensive multi-key lookup since
// different providers behind a Mistral-compatible endpoint spell this field
// differently. First present field wins (matching JS's `??` chain, which
// treats an explicit 0 as present), then clamped to [0, promptTokens].
func getMistralCachedPromptTokens(u mistralUsageWire, promptTokens int) int {
	lookups := []func() (int, bool){
		u.PromptTokensDetails.cached,
		u.PromptTokensDetails_.cached,
		u.PromptTokenDetails.cached,
		u.PromptTokenDetails_.cached,
		func() (int, bool) {
			if u.NumCachedTokens != nil {
				return *u.NumCachedTokens, true
			}
			return 0, false
		},
		func() (int, bool) {
			if u.NumCachedTokens_ != nil {
				return *u.NumCachedTokens_, true
			}
			return 0, false
		},
	}
	cached := 0
	for _, lookup := range lookups {
		if v, ok := lookup(); ok {
			cached = v
			break
		}
	}
	if cached < 0 {
		cached = 0
	}
	if cached > promptTokens {
		cached = promptTokens
	}
	return cached
}

type mistralChunk struct {
	ID      string `json:"id"`
	Choices []struct {
		Delta        mistralDelta `json:"delta"`
		FinishReason string       `json:"finish_reason"`
	} `json:"choices"`
	Usage *mistralUsageWire `json:"usage"`
}

// mapMistralStopReason mirrors mapChatStopReason (mistral-conversations.js:731-747).
func mapMistralStopReason(reason string) (msg.StopReason, string) {
	switch reason {
	case "stop":
		return msg.StopStop, ""
	case "length", "model_length":
		return msg.StopLength, ""
	case "tool_calls":
		return msg.StopToolUse, ""
	case "error":
		return msg.StopError, "Provider stopped with: error"
	default:
		return msg.StopError, fmt.Sprintf("Provider stopped with: %s", reason)
	}
}

// Stream starts a Mistral chat-completions completion.
func (c *MistralConversationsClient) Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions, auth Auth) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	events := make(chan msg.StreamEvent, 16)
	done := make(chan struct{})
	var final *msg.AssistantMessage
	var finalErr error

	crash.Go(func() {
		final, finalErr = c.run(ctx, model, transcript, opts, auth, events)
		// Not deferred: a panic in run must reach the guard before
		// wait() returns a nil message to the lane.
		close(done)
		close(events)
	})

	wait := func() (*msg.AssistantMessage, error) {
		<-done
		return final, finalErr
	}
	return events, wait
}

func (c *MistralConversationsClient) run(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions, auth Auth, events chan<- msg.StreamEvent) (*msg.AssistantMessage, error) {
	partial := &msg.AssistantMessage{
		API:        string(provider.ApiMistralConversations),
		Provider:   model.Provider,
		Model:      model.ID,
		Role:       msg.RoleAssistant,
		StopReason: msg.StopPending,
		Content:    msg.Blocks{},
	}

	normalizer := newMistralToolCallIDNormalizer()
	wireReq := buildMistralRequest(model, transcript, opts, normalizer.normalize)
	body, err := json.Marshal(wireReq)
	if err != nil {
		return errorOut(partial, events, false, err)
	}

	url := strings.TrimRight(model.BaseURL, "/") + "/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return errorOut(partial, events, false, err)
	}
	httpReq.Header = mistralHeaders(model, auth)

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

	// currentPos/currentKind track the single open text-or-thinking block,
	// matching pi's currentBlock state machine (consumeChatStream,
	// mistral-conversations.js:394-570): at most one open text/thinking
	// block at a time, closed and reopened whenever the type changes or a
	// tool call arrives.
	currentPos := -1
	currentKind := ""
	finishCurrent := func() {
		if currentPos == -1 {
			return
		}
		switch currentKind {
		case "text":
			tc := partial.Content[currentPos].(msg.TextContent)
			events <- msg.StreamEvent{Type: msg.EventTextEnd, ContentIndex: currentPos, Content: tc.Text, Partial: partial}
		case "thinking":
			tc := partial.Content[currentPos].(msg.ThinkingContent)
			events <- msg.StreamEvent{Type: msg.EventThinkingEnd, ContentIndex: currentPos, Content: tc.Thinking, Partial: partial}
		}
		currentPos = -1
		currentKind = ""
	}

	toolBlocksByKey := map[string]int{}
	toolArgsBuf := map[string]string{}
	var toolOrder []int
	sawFinish := false

	r := bufio.NewReader(resp.Body)
	streamErr := scanSSE(r, func(ev sseEvent) {
		data := strings.TrimSpace(ev.Data)
		if data == "" || data == "[DONE]" {
			return
		}
		var chunk mistralChunk
		if json.Unmarshal([]byte(data), &chunk) != nil {
			return
		}

		// output.responseId ||= chunk.id -- keep the first non-empty id.
		if partial.ResponseID == "" && chunk.ID != "" {
			partial.ResponseID = chunk.ID
		}

		if chunk.Usage != nil {
			promptTokens := chunk.Usage.PromptTokens
			cachedPromptTokens := getMistralCachedPromptTokens(*chunk.Usage, promptTokens)
			input := promptTokens - cachedPromptTokens
			if input < 0 {
				input = 0
			}
			partial.Usage.Input = input
			partial.Usage.Output = chunk.Usage.CompletionTokens
			partial.Usage.CacheRead = cachedPromptTokens
			partial.Usage.CacheWrite = 0
			total := chunk.Usage.TotalTokens
			if total == 0 {
				total = partial.Usage.Input + partial.Usage.Output + partial.Usage.CacheRead + partial.Usage.CacheWrite
			}
			partial.Usage.TotalTokens = total
			computeAnthropicCost(model, &partial.Usage) // rate math is provider-agnostic
		}

		if len(chunk.Choices) == 0 {
			return
		}
		choice := chunk.Choices[0]

		if choice.FinishReason != "" {
			sawFinish = true
			partial.RawStopReason = choice.FinishReason
			reason, errMsg := mapMistralStopReason(choice.FinishReason)
			partial.StopReason = reason
			if errMsg != "" {
				partial.ErrorMessage = errMsg
			}
		}

		delta := choice.Delta
		if !delta.Content.IsNull {
			for _, item := range delta.Content.Items {
				switch item.Kind {
				case "text":
					if currentKind != "text" {
						finishCurrent()
						partial.Content = append(partial.Content, msg.Text(""))
						currentPos = len(partial.Content) - 1
						currentKind = "text"
						events <- msg.StreamEvent{Type: msg.EventTextStart, ContentIndex: currentPos, Partial: partial}
					}
					tc := partial.Content[currentPos].(msg.TextContent)
					tc.Text += item.Text
					partial.Content[currentPos] = tc
					events <- msg.StreamEvent{Type: msg.EventTextDelta, ContentIndex: currentPos, Delta: item.Text, Partial: partial}
				case "thinking":
					if item.Text == "" {
						continue
					}
					if currentKind != "thinking" {
						finishCurrent()
						partial.Content = append(partial.Content, msg.Thinking(""))
						currentPos = len(partial.Content) - 1
						currentKind = "thinking"
						events <- msg.StreamEvent{Type: msg.EventThinkingStart, ContentIndex: currentPos, Partial: partial}
					}
					tc := partial.Content[currentPos].(msg.ThinkingContent)
					tc.Thinking += item.Text
					partial.Content[currentPos] = tc
					events <- msg.StreamEvent{Type: msg.EventThinkingDelta, ContentIndex: currentPos, Delta: item.Text, Partial: partial}
				}
			}
		}

		if len(delta.ToolCalls) > 0 {
			// Tool calls always interrupt the current text/thinking block.
			if currentKind != "" {
				finishCurrent()
			}
			for _, tcw := range delta.ToolCalls {
				idx := 0
				hasIdx := tcw.Index != nil
				if hasIdx {
					idx = *tcw.Index
				}
				callID := tcw.ID
				if callID == "" || callID == "null" {
					callID = deriveMistralToolCallID(fmt.Sprintf("toolcall:%d", idx), 0)
				}
				var key string
				if hasIdx {
					key = "idx:" + strconv.Itoa(idx)
				} else {
					key = "id:" + callID
				}
				pos, ok := toolBlocksByKey[key]
				if !ok {
					partial.Content = append(partial.Content, msg.NewToolCall(callID, tcw.Function.Name, map[string]any{}))
					pos = len(partial.Content) - 1
					toolBlocksByKey[key] = pos
					toolOrder = append(toolOrder, pos)
					events <- msg.StreamEvent{Type: msg.EventToolCallStart, ContentIndex: pos, Partial: partial}
				}
				argsDelta := string(tcw.Function.Arguments)
				toolArgsBuf[key] += argsDelta
				var args map[string]any
				if json.Unmarshal([]byte(toolArgsBuf[key]), &args) == nil {
					call := partial.Content[pos].(msg.ToolCall)
					call.Arguments = args
					partial.Content[pos] = call
				}
				events <- msg.StreamEvent{Type: msg.EventToolCallDelta, ContentIndex: pos, Delta: argsDelta, Partial: partial}
			}
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

	finishCurrent()
	for _, pos := range toolOrder {
		call := partial.Content[pos].(msg.ToolCall)
		events <- msg.StreamEvent{Type: msg.EventToolCallEnd, ContentIndex: pos, ToolCall: &call, Partial: partial}
	}

	if ctx.Err() != nil {
		return errorOut(partial, events, true, fmt.Errorf("request was aborted"))
	}
	if !sawFinish {
		return errorOut(partial, events, false, fmt.Errorf("mistral stream ended without a finish reason"))
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
