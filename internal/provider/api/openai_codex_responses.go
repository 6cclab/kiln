package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/andrepato/harness/internal/crash"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// OpenAI Codex/ChatGPT-backend Responses API streaming client, ported from
// pi-ai's dist/api/openai-codex-responses.js (request building, headers,
// auth) and dist/api/openai-responses-shared.js (message/tool conversion,
// SSE state machine -- shared with plain OpenAI Responses). This file is
// deliberately self-contained: it does not import anything from a sibling
// openai_responses.go (which may not exist in this package yet), so its
// wire shapes, headers helper and SSE state machine are small, local
// duplicates of the same-shaped pieces there, in the same spirit as
// anthropic_messages.go and openai_completions.go each carrying their own
// copies of this package's conventions.
//
// Scope: only the plain HTTP+SSE transport is implemented. The original
// pi-ai client also supports a WebSocket transport with its own
// retry/backoff/zstd-compression fallback machinery (roughly 90% of
// openai-codex-responses.js's 1302 lines) -- none of that is ported; it is
// out of scope for this phase and is a deviation, not an omission of
// something required.

// DefaultCodexBaseURL is pi's DEFAULT_CODEX_BASE_URL (openai-codex-responses.js:19).
const DefaultCodexBaseURL = "https://chatgpt.com/backend-api"

// codexJWTClaimPath is pi's JWT_CLAIM_PATH (openai-codex-responses.js:21):
// the ChatGPT access token carries the account id nested under this claim.
const codexJWTClaimPath = "https://api.openai.com/auth"

// OpenAICodexResponsesClient streams completions against OpenAI's
// Codex/ChatGPT-backend Responses endpoint (resolveCodexURL(model.BaseURL)).
// Auth.APIKey holds a ChatGPT account access token (a JWT), not a plain API
// key; extractCodexAccountID pulls the chatgpt_account_id claim out of it.
type OpenAICodexResponsesClient struct {
	HTTPClient *http.Client
}

func (c *OpenAICodexResponsesClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// --- endpoint resolution ---

// resolveCodexURL ports pi's resolveCodexUrl (openai-codex-responses.js:458).
func resolveCodexURL(baseURL string) string {
	raw := baseURL
	if strings.TrimSpace(baseURL) == "" {
		raw = DefaultCodexBaseURL
	}
	normalized := strings.TrimRight(raw, "/")
	if strings.HasSuffix(normalized, "/codex/responses") {
		return normalized
	}
	if strings.HasSuffix(normalized, "/codex") {
		return normalized + "/responses"
	}
	return normalized + "/codex/responses"
}

// --- auth: extracting the ChatGPT account id from the JWT access token ---

// codexJWTPayload decodes just the claim pi reads out of the JWT payload
// segment (extractAccountId, openai-codex-responses.js:1250).
type codexJWTPayload struct {
	Auth *struct {
		ChatGPTAccountID string `json:"chatgpt_account_id"`
	} `json:"https://api.openai.com/auth"`
}

// extractCodexAccountID ports pi's extractAccountId. It is a build-time
// (pre-HTTP-request) failure mode: pi throws synchronously before issuing
// any request when the token cannot be parsed.
func extractCodexAccountID(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("failed to extract accountId from token")
	}
	payload, err := decodeJWTSegment(parts[1])
	if err != nil {
		return "", errors.New("failed to extract accountId from token")
	}
	var p codexJWTPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", errors.New("failed to extract accountId from token")
	}
	if p.Auth == nil || p.Auth.ChatGPTAccountID == "" {
		return "", errors.New("failed to extract accountId from token")
	}
	return p.Auth.ChatGPTAccountID, nil
}

// decodeJWTSegment base64url-decodes one JWT segment. JWT segments are
// conventionally unpadded (RawURLEncoding); a padded segment is also
// accepted for robustness.
func decodeJWTSegment(s string) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if m := len(s) % 4; m != 0 {
		s += strings.Repeat("=", 4-m)
	}
	return base64.URLEncoding.DecodeString(s)
}

// --- headers (buildBaseCodexHeaders / buildSSEHeaders) ---

// codexHeaders ports pi's buildSSEHeaders (openai-codex-responses.js:1281),
// SSE-transport fields only (no session-id/x-client-request-id: this phase's
// Auth/StreamOptions carry no sessionId concept, a deviation).
func codexHeaders(model provider.Model, auth Auth, accountID string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+auth.APIKey)
	h.Set("chatgpt-account-id", accountID)
	// "originator: pi" is a protocol identity marker the ChatGPT backend
	// expects from a pi-compatible caller; it is sent verbatim here to speak
	// the same wire protocol, not as a branding claim by this harness.
	h.Set("originator", "pi")
	// pi's getPiUserAgent() is out of scope to replicate exactly; this is a
	// reasonable stand-in literal, noted as a deviation.
	h.Set("User-Agent", "harness/0.1 (+https://github.com/andrepato/harness)")
	h.Set("OpenAI-Beta", "responses=experimental")
	h.Set("Accept", "text/event-stream")
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

// --- request construction (buildRequestBody) ---

type codexTextOpt struct {
	Verbosity string `json:"verbosity"`
}

type codexReasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary,omitempty"`
}

// codexRequest is the wire body pi's buildRequestBody constructs
// (openai-codex-responses.js:373). Input/Tools items are untyped maps
// because the Responses input/tool item shapes are heterogeneous
// discriminated unions in the original TS; a map preserves the exact wire
// shape pi emits without inventing a closed Go type for every item kind.
type codexRequest struct {
	Model             string           `json:"model"`
	Store             bool             `json:"store"`
	Stream            bool             `json:"stream"`
	Instructions      string           `json:"instructions"`
	Input             []map[string]any `json:"input"`
	Text              codexTextOpt     `json:"text"`
	Include           []string         `json:"include"`
	ToolChoice        string           `json:"tool_choice"`
	ParallelToolCalls bool             `json:"parallel_tool_calls"`
	Temperature       *float64         `json:"temperature,omitempty"`
	Tools             []map[string]any `json:"tools,omitempty"`
	Reasoning         *codexReasoning  `json:"reasoning,omitempty"`
	// prompt_cache_key is omitted: no sessionId concept this phase (deviation).
}

// buildCodexRequest ports pi's buildRequestBody for the SSE-transport
// subset described at the top of this file.
func buildCodexRequest(model provider.Model, transcript []msg.Message, opts provider.StreamOptions) codexRequest {
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
	instructions := systemText
	if instructions == "" {
		instructions = "You are a helpful assistant."
	}

	req := codexRequest{
		Model:             model.ID,
		Store:             false,
		Stream:            true,
		Instructions:      instructions,
		Input:             convertCodexMessages(model, transcript),
		Text:              codexTextOpt{Verbosity: "low"},
		Include:           []string{"reasoning.encrypted_content"},
		ToolChoice:        "auto",
		ParallelToolCalls: true,
	}
	if opts.Temperature != nil {
		req.Temperature = opts.Temperature
	}
	if len(opts.Tools) > 0 {
		req.Tools = convertCodexTools(opts.Tools, model.SupportsStrictMode())
	}
	req.Reasoning = buildCodexReasoning(model, opts.ThinkingLevel)
	return req
}

// buildCodexReasoning ports the reasoning-effort branch of buildRequestBody
// (openai-codex-responses.js:414-429), expressed against this phase's
// StreamOptions.ThinkingLevel / provider.ThinkingOff in place of pi's
// separate reasoningEffort/"none" option, and hardcoding
// summary:"auto" since StreamOptions carries no reasoningSummary field
// (both deviations).
func buildCodexReasoning(model provider.Model, level provider.ThinkingLevel) *codexReasoning {
	if level != "" {
		var effort string
		omit := false
		if level == provider.ThinkingOff {
			if mapped, ok := model.ThinkingLevelMap[provider.ThinkingOff]; ok {
				if mapped == nil {
					omit = true
				} else {
					effort = *mapped
				}
			} else {
				effort = "none"
			}
		} else if mapped, ok := model.ThinkingLevelMap[level]; ok {
			if mapped == nil {
				omit = true
			} else {
				effort = *mapped
			}
		} else {
			effort = string(level)
		}
		if omit {
			return nil
		}
		return &codexReasoning{Effort: effort, Summary: "auto"}
	}
	if model.Reasoning {
		if mapped, ok := model.ThinkingLevelMap[provider.ThinkingOff]; ok && mapped == nil {
			return nil
		}
		effort := "none"
		if mapped, ok := model.ThinkingLevelMap[provider.ThinkingOff]; ok && mapped != nil {
			effort = *mapped
		}
		return &codexReasoning{Effort: effort}
	}
	return nil
}

// convertCodexTools ports the in-scope subset of convertResponsesTools
// (openai-responses-shared.js:271): grammar tools, additional_tools and
// tool_search are all default-off and out of scope (deviation).
func convertCodexTools(tools []provider.ToolDef, supportsStrictMode bool) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, td := range tools {
		if len(td.ServerTool) > 0 {
			continue
		}
		schema := td.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		item := map[string]any{
			"type":        "function",
			"name":        td.Name,
			"description": td.Description,
			"parameters":  schema,
		}
		if supportsStrictMode {
			item["strict"] = supportsStrictMode
		}
		out = append(out, item)
	}
	return out
}

// codexSplitToolCallID splits a "callId|itemId" ToolCall.ID / ToolCallID into its
// two parts, matching how pi encodes Responses-API tool call ids
// (openai-responses-shared.js:207,246). If there is no "|", the whole
// string is the call id and the item id is empty.
func codexSplitToolCallID(id string) (callID, itemID string) {
	if i := strings.Index(id, "|"); i >= 0 {
		return id[:i], id[i+1:]
	}
	return id, ""
}

// convertCodexMessages ports the in-scope subset of convertResponsesMessages
// (openai-responses-shared.js:63) with includeSystemPrompt:false: system
// messages are excluded from input entirely here (they become `instructions`
// instead via buildCodexRequest) -- this differs from plain OpenAI
// Responses, which DOES include the leading system message as an input
// item. Grammar tools/additional_tools/tool_search/cross-model id
// normalization are all out of scope (deviations); toolCall ids are passed
// through verbatim, split on "|".
func convertCodexMessages(model provider.Model, transcript []msg.Message) []map[string]any {
	var out []map[string]any
	msgIndex := 0
	for _, m := range transcript {
		switch t := m.(type) {
		case msg.SystemMessage:
			continue // folded into instructions, never an input item
		case msg.UserMessage:
			var content []map[string]any
			for _, b := range t.Content {
				switch c := b.(type) {
				case msg.TextContent:
					content = append(content, map[string]any{"type": "input_text", "text": c.Text})
				case msg.ImageContent:
					content = append(content, map[string]any{
						"type":      "input_image",
						"detail":    "auto",
						"image_url": "data:" + c.MimeType + ";base64," + c.Data,
					})
				}
			}
			if len(content) > 0 {
				out = append(out, map[string]any{"role": "user", "content": content})
			}
		case msg.AssistantMessage:
			textBlockIndex := 0
			for _, b := range t.Content {
				switch c := b.(type) {
				case msg.ThinkingContent:
					if c.ThinkingSignature == "" {
						// No signature to replay: pi requires one to
						// round-trip a reasoning item; without it the
						// block is dropped (matches pi's
						// parseTextSignature/signature-required
						// behavior for reasoning items -- not a
						// deviation, an expected drop).
						continue
					}
					var raw map[string]any
					if json.Unmarshal([]byte(c.ThinkingSignature), &raw) != nil {
						continue // not valid JSON: can't replay verbatim
					}
					out = append(out, raw)
				case msg.TextContent:
					id := fmt.Sprintf("msg_pi_%d", msgIndex)
					if textBlockIndex > 0 {
						id = fmt.Sprintf("msg_pi_%d_%d", msgIndex, textBlockIndex)
					}
					textBlockIndex++
					out = append(out, map[string]any{
						"type": "message",
						"role": "assistant",
						"content": []map[string]any{
							{"type": "output_text", "text": c.Text, "annotations": []any{}},
						},
						"status": "completed",
						"id":     id,
					})
				case msg.ToolCall:
					callID, itemID := codexSplitToolCallID(c.ID)
					args, _ := json.Marshal(c.Arguments)
					item := map[string]any{
						"type":      "function_call",
						"call_id":   callID,
						"name":      c.Name,
						"arguments": string(args),
					}
					if itemID != "" {
						item["id"] = itemID
					}
					out = append(out, item)
				}
			}
		case msg.ToolResultMessage:
			callID, _ := codexSplitToolCallID(t.ToolCallID)
			out = append(out, map[string]any{
				"type":    "function_call_output",
				"call_id": callID,
				"output":  convertCodexToolResultOutput(model, t.Content),
			})
		}
		msgIndex++
	}
	return out
}

// convertCodexToolResultOutput ports convertToolResultOutput
// (openai-responses-shared.js:37).
func convertCodexToolResultOutput(model provider.Model, content msg.Blocks) any {
	var textParts []string
	var images []msg.ImageContent
	for _, b := range content {
		switch c := b.(type) {
		case msg.TextContent:
			textParts = append(textParts, c.Text)
		case msg.ImageContent:
			images = append(images, c)
		}
	}
	text := strings.Join(textParts, "\n")
	hasText := text != ""

	supportsImage := false
	for _, in := range model.Input {
		if in == "image" {
			supportsImage = true
			break
		}
	}

	if len(images) == 0 || !supportsImage {
		if hasText {
			return text
		}
		if len(images) > 0 {
			return "(see attached image)"
		}
		return "(no tool output)"
	}

	var out []map[string]any
	if hasText {
		out = append(out, map[string]any{"type": "input_text", "text": text})
	}
	for _, img := range images {
		out = append(out, map[string]any{
			"type":      "input_image",
			"detail":    "auto",
			"image_url": "data:" + img.MimeType + ";base64," + img.Data,
		})
	}
	return out
}

// --- response wire shapes / SSE state machine (processResponsesStream) ---

type codexItem struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Summary   []struct {
		Text string `json:"text"`
	} `json:"summary"`
	Content []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
}

type codexSSEOutputItemEnvelope struct {
	OutputIndex int             `json:"output_index"`
	Item        json.RawMessage `json:"item"`
}

type codexSSEDelta struct {
	OutputIndex int    `json:"output_index"`
	Delta       string `json:"delta"`
}

type codexSSEFunctionCallArgsDone struct {
	OutputIndex int    `json:"output_index"`
	Arguments   string `json:"arguments"`
}

type codexUsageDetails struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

type codexSSEResponseEnvelope struct {
	Response struct {
		ID                string `json:"id"`
		Status            string `json:"status"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Usage *codexUsageDetails `json:"usage"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"response"`
}

// mapCodexStopReason ports pi's mapStopReason
// (openai-responses-shared.js:660), shared verbatim by plain OpenAI
// Responses and Codex.
func mapCodexStopReason(status, incompleteReason string) (msg.StopReason, string) {
	if status == "" {
		return msg.StopStop, ""
	}
	switch status {
	case "completed":
		return msg.StopStop, ""
	case "incomplete":
		if incompleteReason == "max_output_tokens" {
			return msg.StopLength, ""
		}
		if incompleteReason != "" {
			return msg.StopError, fmt.Sprintf("Response incomplete: %s", incompleteReason)
		}
		return msg.StopError, "Response incomplete without a provider reason"
	case "failed", "cancelled":
		return msg.StopError, ""
	case "in_progress", "queued":
		return msg.StopStop, ""
	default:
		return msg.StopError, fmt.Sprintf("unhandled stop reason: %s", status)
	}
}

func codexHasToolCall(blocks msg.Blocks) bool {
	for _, b := range blocks {
		if _, ok := b.(msg.ToolCall); ok {
			return true
		}
	}
	return false
}

// Stream starts an OpenAI Codex Responses completion. The returned channel
// is closed once the stream ends; wait blocks for that and returns the
// final message or the terminating error.
func (c *OpenAICodexResponsesClient) Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions, auth Auth) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
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

// codexSlot tracks one in-flight output item, mirroring processResponsesStream's
// outputSlots map (openai-responses-shared.js:322).
type codexSlot struct {
	kind        string // "thinking" | "text" | "toolCall"
	pos         int
	partialJSON string
}

func (c *OpenAICodexResponsesClient) run(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions, auth Auth, events chan<- msg.StreamEvent) (*msg.AssistantMessage, error) {
	partial := &msg.AssistantMessage{
		API:        string(provider.ApiOpenAICodexResponses),
		Provider:   model.Provider,
		Model:      model.ID,
		Role:       msg.RoleAssistant,
		StopReason: msg.StopPending,
		Content:    msg.Blocks{},
	}

	accountID, err := extractCodexAccountID(auth.APIKey)
	if err != nil {
		return errorOut(partial, events, false, err)
	}

	wireReq := buildCodexRequest(model, transcript, opts)
	body, err := json.Marshal(wireReq)
	if err != nil {
		return errorOut(partial, events, false, err)
	}

	url := resolveCodexURL(model.BaseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return errorOut(partial, events, false, err)
	}
	httpReq.Header = codexHeaders(model, auth, accountID)

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

	slots := map[int]*codexSlot{}
	terminated := false

	r := bufio.NewReader(resp.Body)
	streamErr := scanSSE(r, func(ev sseEvent) {
		if terminated {
			return
		}
		if ev.Data == "" {
			return
		}
		switch ev.Event {
		case "response.created":
			var env struct {
				Response struct {
					ID string `json:"id"`
				} `json:"response"`
			}
			if json.Unmarshal([]byte(ev.Data), &env) == nil {
				partial.ResponseID = env.Response.ID
			}

		case "response.output_item.added":
			var env codexSSEOutputItemEnvelope
			if json.Unmarshal([]byte(ev.Data), &env) != nil {
				return
			}
			var item codexItem
			if json.Unmarshal(env.Item, &item) != nil {
				return
			}
			switch item.Type {
			case "reasoning":
				partial.Content = append(partial.Content, msg.Thinking(""))
				pos := len(partial.Content) - 1
				slots[env.OutputIndex] = &codexSlot{kind: "thinking", pos: pos}
				events <- msg.StreamEvent{Type: msg.EventThinkingStart, ContentIndex: pos, Partial: partial}
			case "message":
				partial.Content = append(partial.Content, msg.Text(""))
				pos := len(partial.Content) - 1
				slots[env.OutputIndex] = &codexSlot{kind: "text", pos: pos}
				events <- msg.StreamEvent{Type: msg.EventTextStart, ContentIndex: pos, Partial: partial}
			case "function_call":
				id := item.CallID
				if item.ID != "" {
					id = item.CallID + "|" + item.ID
				}
				var args map[string]any
				if item.Arguments != "" {
					_ = json.Unmarshal([]byte(item.Arguments), &args)
				}
				partial.Content = append(partial.Content, msg.NewToolCall(id, item.Name, args))
				pos := len(partial.Content) - 1
				slots[env.OutputIndex] = &codexSlot{kind: "toolCall", pos: pos, partialJSON: item.Arguments}
				events <- msg.StreamEvent{Type: msg.EventToolCallStart, ContentIndex: pos, Partial: partial}
			}

		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			var d codexSSEDelta
			if json.Unmarshal([]byte(ev.Data), &d) != nil {
				return
			}
			slot, ok := slots[d.OutputIndex]
			if !ok || slot.kind != "thinking" {
				return
			}
			tc := partial.Content[slot.pos].(msg.ThinkingContent)
			tc.Thinking += d.Delta
			partial.Content[slot.pos] = tc
			events <- msg.StreamEvent{Type: msg.EventThinkingDelta, ContentIndex: slot.pos, Delta: d.Delta, Partial: partial}

		case "response.reasoning_summary_part.done":
			var d codexSSEDelta
			if json.Unmarshal([]byte(ev.Data), &d) != nil {
				return
			}
			slot, ok := slots[d.OutputIndex]
			if !ok || slot.kind != "thinking" {
				return
			}
			tc := partial.Content[slot.pos].(msg.ThinkingContent)
			tc.Thinking += "\n\n"
			partial.Content[slot.pos] = tc
			events <- msg.StreamEvent{Type: msg.EventThinkingDelta, ContentIndex: slot.pos, Delta: "\n\n", Partial: partial}

		case "response.output_text.delta", "response.refusal.delta":
			var d codexSSEDelta
			if json.Unmarshal([]byte(ev.Data), &d) != nil {
				return
			}
			slot, ok := slots[d.OutputIndex]
			if !ok || slot.kind != "text" {
				return
			}
			tc := partial.Content[slot.pos].(msg.TextContent)
			tc.Text += d.Delta
			partial.Content[slot.pos] = tc
			events <- msg.StreamEvent{Type: msg.EventTextDelta, ContentIndex: slot.pos, Delta: d.Delta, Partial: partial}

		case "response.function_call_arguments.delta":
			var d codexSSEDelta
			if json.Unmarshal([]byte(ev.Data), &d) != nil {
				return
			}
			slot, ok := slots[d.OutputIndex]
			if !ok || slot.kind != "toolCall" {
				return
			}
			slot.partialJSON += d.Delta
			tc := partial.Content[slot.pos].(msg.ToolCall)
			var args map[string]any
			if json.Unmarshal([]byte(slot.partialJSON), &args) == nil {
				tc.Arguments = args
			}
			partial.Content[slot.pos] = tc
			events <- msg.StreamEvent{Type: msg.EventToolCallDelta, ContentIndex: slot.pos, Delta: d.Delta, Partial: partial}

		case "response.function_call_arguments.done":
			var d codexSSEFunctionCallArgsDone
			if json.Unmarshal([]byte(ev.Data), &d) != nil {
				return
			}
			slot, ok := slots[d.OutputIndex]
			if !ok || slot.kind != "toolCall" {
				return
			}
			previous := slot.partialJSON
			slot.partialJSON = d.Arguments
			tc := partial.Content[slot.pos].(msg.ToolCall)
			var args map[string]any
			if json.Unmarshal([]byte(slot.partialJSON), &args) == nil {
				tc.Arguments = args
			}
			partial.Content[slot.pos] = tc
			if strings.HasPrefix(d.Arguments, previous) {
				if suffix := d.Arguments[len(previous):]; suffix != "" {
					events <- msg.StreamEvent{Type: msg.EventToolCallDelta, ContentIndex: slot.pos, Delta: suffix, Partial: partial}
				}
			}

		case "response.output_item.done":
			var env codexSSEOutputItemEnvelope
			if json.Unmarshal([]byte(ev.Data), &env) != nil {
				return
			}
			var item codexItem
			if json.Unmarshal(env.Item, &item) != nil {
				return
			}
			slot, ok := slots[env.OutputIndex]
			if !ok {
				return
			}
			switch item.Type {
			case "reasoning":
				if slot.kind != "thinking" {
					return
				}
				var summaryParts, contentParts []string
				for _, s := range item.Summary {
					summaryParts = append(summaryParts, s.Text)
				}
				for _, ct := range item.Content {
					contentParts = append(contentParts, ct.Text)
				}
				tc := partial.Content[slot.pos].(msg.ThinkingContent)
				if s := strings.Join(summaryParts, "\n\n"); s != "" {
					tc.Thinking = s
				} else if s := strings.Join(contentParts, "\n\n"); s != "" {
					tc.Thinking = s
				}
				tc.ThinkingSignature = string(env.Item)
				partial.Content[slot.pos] = tc
				events <- msg.StreamEvent{Type: msg.EventThinkingEnd, ContentIndex: slot.pos, Content: tc.Thinking, Partial: partial}
				delete(slots, env.OutputIndex)
			case "message":
				if slot.kind != "text" {
					return
				}
				var parts []string
				for _, ct := range item.Content {
					if ct.Type == "output_text" {
						parts = append(parts, ct.Text)
					} else {
						parts = append(parts, ct.Refusal)
					}
				}
				tc := partial.Content[slot.pos].(msg.TextContent)
				tc.Text = strings.Join(parts, "")
				partial.Content[slot.pos] = tc
				events <- msg.StreamEvent{Type: msg.EventTextEnd, ContentIndex: slot.pos, Content: tc.Text, Partial: partial}
				delete(slots, env.OutputIndex)
			case "function_call":
				if slot.kind != "toolCall" {
					return
				}
				finalArgs := item.Arguments
				if finalArgs == "" {
					finalArgs = slot.partialJSON
				}
				if finalArgs == "" {
					finalArgs = "{}"
				}
				tc := partial.Content[slot.pos].(msg.ToolCall)
				var args map[string]any
				if json.Unmarshal([]byte(finalArgs), &args) == nil {
					tc.Arguments = args
				}
				partial.Content[slot.pos] = tc
				events <- msg.StreamEvent{Type: msg.EventToolCallEnd, ContentIndex: slot.pos, ToolCall: &tc, Partial: partial}
				delete(slots, env.OutputIndex)
			}

		case "response.completed", "response.incomplete":
			var env codexSSEResponseEnvelope
			if json.Unmarshal([]byte(ev.Data), &env) != nil {
				return
			}
			if env.Response.ID != "" {
				partial.ResponseID = env.Response.ID
			}
			if env.Response.Usage != nil {
				u := env.Response.Usage
				cached := u.InputTokensDetails.CachedTokens
				cacheWrite := u.InputTokensDetails.CacheWriteTokens
				input := u.InputTokens - cached - cacheWrite
				if input < 0 {
					input = 0
				}
				partial.Usage.Input = input
				partial.Usage.Output = u.OutputTokens
				partial.Usage.CacheRead = cached
				partial.Usage.CacheWrite = cacheWrite
				reasoning := u.OutputTokensDetails.ReasoningTokens
				partial.Usage.Reasoning = &reasoning
				partial.Usage.TotalTokens = u.TotalTokens
				computeAnthropicCost(model, &partial.Usage)
			}
			incompleteReason := ""
			if env.Response.IncompleteDetails != nil {
				incompleteReason = env.Response.IncompleteDetails.Reason
			}
			status := env.Response.Status
			if incompleteReason != "" {
				partial.RawStopReason = status + "." + incompleteReason
			} else {
				partial.RawStopReason = status
			}
			reason, errMsg := mapCodexStopReason(status, incompleteReason)
			partial.StopReason = reason
			if errMsg != "" {
				partial.ErrorMessage = errMsg
			}
			if codexHasToolCall(partial.Content) && partial.StopReason == msg.StopStop {
				partial.StopReason = msg.StopToolUse
			}
			terminated = true

		case "response.failed":
			var env codexSSEResponseEnvelope
			if json.Unmarshal([]byte(ev.Data), &env) != nil {
				return
			}
			partial.RawStopReason = env.Response.Status
			var errMsg string
			switch {
			case env.Response.Error != nil:
				code := env.Response.Error.Code
				if code == "" {
					code = "unknown"
				}
				message := env.Response.Error.Message
				if message == "" {
					message = "no message"
				}
				errMsg = code + ": " + message
			case env.Response.IncompleteDetails != nil && env.Response.IncompleteDetails.Reason != "":
				errMsg = "incomplete: " + env.Response.IncompleteDetails.Reason
			default:
				errMsg = "Unknown error (no error details in response)"
			}
			partial.StopReason = msg.StopError
			partial.ErrorMessage = errMsg
			terminated = true

		case "error":
			var e struct {
				Code    any    `json:"code"`
				Message string `json:"message"`
			}
			if json.Unmarshal([]byte(ev.Data), &e) != nil {
				return
			}
			partial.StopReason = msg.StopError
			partial.ErrorMessage = fmt.Sprintf("Error Code %v: %s", e.Code, e.Message)
			terminated = true
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
		return errorOut(partial, events, false, fmt.Errorf("codex stream ended without a stop reason"))
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
