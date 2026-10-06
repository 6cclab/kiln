package api

// Azure OpenAI Responses API streaming client, ported from pi-ai's
// dist/api/azure-openai-responses.js (request construction: buildParams) and
// dist/api/openai-responses-shared.js (convertResponsesMessages,
// convertResponsesTools, processResponsesStream -- shared with plain
// OpenAI-responses and Codex-responses, but re-implemented here
// standalone per package convention: every client in this package is
// self-contained, duplicating the small amount of shared SSE-parsing logic
// locally rather than importing a sibling client file).
//
// Azure's own buildParams is deliberately simpler than plain OpenAI
// Responses's: no prompt_cache_retention/prompt_cache_options/service_tier/
// session-affinity, no OPENAI_RESPONSES_MIN_OUTPUT_TOKENS floor *gating*
// (the floor itself, Math.max(maxTokens, 16), is still applied -- verified
// by reading azure-openai-responses.js's buildParams directly), and a
// default of supportsStrictMode=true (vs. false for plain OpenAI Responses).
//
// Azure-specific: reasoning.encrypted_content can arrive only on
// response.completed's response.output, not on the reasoning item's own
// output_item.done. backfillReasoningSignatures (in
// openai-responses-shared.js's processResponsesStream, right above
// finalizeResponse) patches the persisted ThinkingSignature from the
// terminal response before the stream ends, so store:false multi-turn
// replay stays stateless. See
// https://github.com/earendil-works/pi/issues/6409.
//
// Deviations from pi-ai this phase (StreamOptions has no matching field):
//   - api-version is hardcoded to "v1" (pi: AZURE_OPENAI_API_VERSION env /
//     options.azureApiVersion override).
//   - deployment name is always model.ID (pi:
//     AZURE_OPENAI_DEPLOYMENT_NAME_MAP env / options.azureDeploymentName
//     override, via resolveDeploymentName).
//   - no prompt_cache_key (pi: clampOpenAIPromptCacheKey(options?.sessionId)
//     -- this harness's StreamOptions has no sessionId).
//   - no tool_choice (pi: options?.toolChoice).
//   - reasoning-effort-without-summary falls back to the openai_completions.go/
//     anthropic_messages.go ThinkingLevelMap idiom; pi's "reasoningSummary
//     alone, no reasoningEffort" case (effort defaults to "medium") cannot
//     arise here since StreamOptions has no separate reasoningSummary field.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/andrepato/harness/internal/crash"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// AzureOpenAIResponsesClient streams completions against an Azure OpenAI
// Responses endpoint (normalizeAzureBaseURL(model.BaseURL) + "/responses",
// with deployment routing carried in the body's "model" field rather than a
// URL path segment -- the Responses endpoint is not in the openai SDK's
// _deployments_endpoints rewrite set, unlike Azure's older chat-completions
// endpoint).
type AzureOpenAIResponsesClient struct {
	HTTPClient *http.Client
}

func (c *AzureOpenAIResponsesClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// --- URL construction ---

// normalizeAzureBaseURL ports pi's normalizeAzureBaseUrl (
// azure-openai-responses.js). Azure hosts (*.openai.azure.com,
// *.cognitiveservices.azure.com, *.ai.azure.com) with a bare, "/openai" or
// "/openai/v1/responses" path are rewritten to "/openai/v1"; every other
// base URL is returned as-is (trailing slash trimmed).
func normalizeAzureBaseURL(raw string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid Azure OpenAI base URL: %s", raw)
	}

	host := u.Hostname()
	isAzureHost := strings.HasSuffix(host, ".openai.azure.com") ||
		strings.HasSuffix(host, ".cognitiveservices.azure.com") ||
		strings.HasSuffix(host, ".ai.azure.com")

	normalizedPath := strings.TrimRight(u.Path, "/")
	if isAzureHost && (normalizedPath == "" || normalizedPath == "/openai" || normalizedPath == "/openai/v1/responses") {
		u.Path = "/openai/v1"
		u.RawQuery = ""
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// azureResponsesURL is normalizeAzureBaseURL(model.BaseURL) + "/responses",
// with the hardcoded default api-version ("v1" -- pi's DEFAULT_AZURE_API_VERSION;
// AZURE_OPENAI_API_VERSION / options.azureApiVersion overrides are out of
// scope this phase, see file header).
func azureResponsesURL(model provider.Model) (string, error) {
	base, err := normalizeAzureBaseURL(model.BaseURL)
	if err != nil {
		return "", err
	}
	return base + "/responses?api-version=v1", nil
}

// --- wire request shapes ---

type azureReasoningWire struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary,omitempty"`
}

type azureToolWire struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	// Strict is only present at all when compat.SupportsStrictMode (default
	// true for Azure) is true; its value is model.SupportsStrictMode()
	// (default false), matching openai_completions.go's convention -- pi's
	// deeper resolveJsonSchemaStrictSampling per-schema resolution is out of
	// scope this phase.
	Strict *bool `json:"strict,omitempty"`
}

type azureRequest struct {
	Model           string              `json:"model"`
	Input           []json.RawMessage   `json:"input"`
	Stream          bool                `json:"stream"`
	Store           bool                `json:"store"`
	MaxOutputTokens int                 `json:"max_output_tokens,omitempty"`
	Temperature     *float64            `json:"temperature,omitempty"`
	Tools           []azureToolWire     `json:"tools,omitempty"`
	Reasoning       *azureReasoningWire `json:"reasoning,omitempty"`
	Include         []string            `json:"include,omitempty"`
}

// --- request construction ---

type azureInputTextPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type azureInputImagePart struct {
	Type     string `json:"type"`
	Detail   string `json:"detail"`
	ImageURL string `json:"image_url"`
}

type azureRoleMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type azureOutputTextPart struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Annotations []any  `json:"annotations"`
}

type azureAssistantMessageItem struct {
	Type    string                `json:"type"`
	Role    string                `json:"role"`
	Content []azureOutputTextPart `json:"content"`
	Status  string                `json:"status"`
	ID      string                `json:"id"`
}

type azureFunctionCallItem struct {
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type azureFunctionCallOutputItem struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output any    `json:"output"`
}

// splitToolCallID splits a ToolCall.ID / ToolResultMessage.ToolCallID of
// shape "<call_id>|<item_id>" into its parts. If there is no "|", the whole
// id is the call id and the item id is empty.
func splitToolCallID(id string) (callID, itemID string) {
	if i := strings.Index(id, "|"); i >= 0 {
		return id[:i], id[i+1:]
	}
	return id, ""
}

// buildAzureInput ports the in-scope subset of convertResponsesMessages
// (openai-responses-shared.js): grammar tools, additional_tools and
// tool_search are out of scope (default off), matching the task contract.
func buildAzureInput(model provider.Model, transcript []msg.Message, opts provider.StreamOptions) []json.RawMessage {
	compat := model.OpenAIResponsesCompat()
	instructionRole := "system"
	if model.Reasoning && boolDefault(compat.SupportsDeveloperRole, true) {
		instructionRole = "developer"
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

	var items []json.RawMessage
	marshal := func(v any) {
		b, err := json.Marshal(v)
		if err != nil {
			return
		}
		items = append(items, b)
	}

	if systemText != "" {
		marshal(azureRoleMessage{Role: instructionRole, Content: systemText})
	}

	msgIndex := 0
	for _, m := range transcript {
		switch t := m.(type) {
		case msg.SystemMessage:
			// folded into the leading instruction message above.
		case msg.UserMessage:
			marshal(azureRoleMessage{Role: "user", Content: convertAzureUserBlocks(t.Content)})
			msgIndex++
		case msg.AssistantMessage:
			textBlockIndex := 0
			for _, b := range t.Content {
				switch c := b.(type) {
				case msg.TextContent:
					id := fmt.Sprintf("msg_pi_%d", msgIndex)
					if textBlockIndex > 0 {
						id = fmt.Sprintf("msg_pi_%d_%d", msgIndex, textBlockIndex)
					}
					textBlockIndex++
					marshal(azureAssistantMessageItem{
						Type:    "message",
						Role:    "assistant",
						Content: []azureOutputTextPart{{Type: "output_text", Text: c.Text, Annotations: []any{}}},
						Status:  "completed",
						ID:      id,
					})
				case msg.ThinkingContent:
					if c.ThinkingSignature != "" && json.Valid([]byte(c.ThinkingSignature)) {
						items = append(items, json.RawMessage(c.ThinkingSignature))
					}
					// else: drop the block, matching pi (no fallback text
					// rendering of thinking without a signature this phase).
				case msg.ToolCall:
					callID, itemID := splitToolCallID(c.ID)
					args, _ := json.Marshal(c.Arguments)
					marshal(azureFunctionCallItem{
						Type:      "function_call",
						ID:        itemID,
						CallID:    callID,
						Name:      c.Name,
						Arguments: string(args),
					})
				}
			}
			msgIndex++
		case msg.ToolResultMessage:
			callID, _ := splitToolCallID(t.ToolCallID)
			marshal(azureFunctionCallOutputItem{
				Type:   "function_call_output",
				CallID: callID,
				Output: convertAzureToolResultOutput(model, t.Content),
			})
			msgIndex++
		}
	}
	return items
}

func convertAzureUserBlocks(blocks msg.Blocks) []any {
	parts := make([]any, 0, len(blocks))
	for _, b := range blocks {
		switch c := b.(type) {
		case msg.TextContent:
			parts = append(parts, azureInputTextPart{Type: "input_text", Text: c.Text})
		case msg.ImageContent:
			parts = append(parts, azureInputImagePart{Type: "input_image", Detail: "auto", ImageURL: "data:" + c.MimeType + ";base64," + c.Data})
		}
	}
	return parts
}

// convertAzureToolResultOutput ports convertToolResultOutput
// (openai-responses-shared.js).
func convertAzureToolResultOutput(model provider.Model, blocks msg.Blocks) any {
	var text string
	var images []msg.ImageContent
	for _, b := range blocks {
		switch c := b.(type) {
		case msg.TextContent:
			if text != "" {
				text += "\n"
			}
			text += c.Text
		case msg.ImageContent:
			images = append(images, c)
		}
	}
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
	var parts []any
	if hasText {
		parts = append(parts, azureInputTextPart{Type: "input_text", Text: text})
	}
	for _, img := range images {
		parts = append(parts, azureInputImagePart{Type: "input_image", Detail: "auto", ImageURL: "data:" + img.MimeType + ";base64," + img.Data})
	}
	return parts
}

// buildAzureTools ports the in-scope subset of convertResponsesTools.
func buildAzureTools(model provider.Model, opts provider.StreamOptions) []azureToolWire {
	if len(opts.Tools) == 0 {
		return nil
	}
	compat := model.OpenAIResponsesCompat()
	// Azure's own default for this gate is true, unlike OpenAIResponsesCompat's
	// generic default of false (verified in azure-openai-responses.js's
	// buildParams: `model.compat?.supportsStrictMode ?? true`).
	supportsStrictGate := boolDefault(compat.SupportsStrictMode, true)
	out := make([]azureToolWire, 0, len(opts.Tools))
	for _, td := range opts.Tools {
		if len(td.ServerTool) > 0 {
			continue
		}
		schema := td.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		tool := azureToolWire{Type: "function", Name: td.Name, Description: td.Description, Parameters: schema}
		if supportsStrictGate {
			v := model.SupportsStrictMode()
			tool.Strict = &v
		}
		out = append(out, tool)
	}
	return out
}

// buildAzureRequest ports buildParams (azure-openai-responses.js).
func buildAzureRequest(model provider.Model, transcript []msg.Message, opts provider.StreamOptions) azureRequest {
	req := azureRequest{
		Model:  model.ID, // deployment name fallback: no AZURE_OPENAI_DEPLOYMENT_NAME_MAP this phase.
		Input:  buildAzureInput(model, transcript, opts),
		Stream: true,
		Store:  false,
	}

	// pi: `if (options?.maxTokens) params.max_output_tokens =
	// Math.max(options.maxTokens, OPENAI_RESPONSES_MIN_OUTPUT_TOKENS)`. Unlike
	// anthropic_messages.go/openai_completions.go, Azure's buildParams does
	// NOT fall back to model.maxTokens when options.maxTokens is unset.
	if opts.MaxTokens > 0 {
		mt := opts.MaxTokens
		if mt < 16 {
			mt = 16
		}
		req.MaxOutputTokens = mt
	}

	if opts.Temperature != nil {
		req.Temperature = opts.Temperature
	}

	req.Tools = buildAzureTools(model, opts)

	if model.Reasoning {
		if opts.ThinkingLevel != "" && opts.ThinkingLevel != provider.ThinkingOff {
			effort := string(opts.ThinkingLevel)
			if model.ThinkingLevelMap != nil {
				if mapped, ok := model.ThinkingLevelMap[opts.ThinkingLevel]; ok && mapped != nil {
					effort = *mapped
				}
			}
			req.Reasoning = &azureReasoningWire{Effort: effort, Summary: "auto"}
			req.Include = []string{"reasoning.encrypted_content"}
		} else {
			offEffort := "none"
			skip := false
			if model.ThinkingLevelMap != nil {
				if mapped, ok := model.ThinkingLevelMap[provider.ThinkingOff]; ok {
					if mapped == nil {
						skip = true
					} else {
						offEffort = *mapped
					}
				}
			}
			if !skip {
				req.Reasoning = &azureReasoningWire{Effort: offEffort}
			}
		}
	}

	return req
}

func azureHeaders(model provider.Model, auth Auth) http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "text/event-stream")
	h.Set("api-key", auth.APIKey)
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

type azureItemSummaryPart struct {
	Text string `json:"text"`
}

type azureItemContentPart struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

type azureItemWire struct {
	Type             string                 `json:"type"`
	ID               string                 `json:"id"`
	CallID           string                 `json:"call_id"`
	Name             string                 `json:"name"`
	Arguments        string                 `json:"arguments"`
	Summary          []azureItemSummaryPart `json:"summary,omitempty"`
	Content          []azureItemContentPart `json:"content,omitempty"`
	EncryptedContent string                 `json:"encrypted_content,omitempty"`
}

type azureUsageDetails struct {
	CachedTokens     int `json:"cached_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

type azureOutputTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

type azureUsageWire struct {
	InputTokens         int                       `json:"input_tokens"`
	OutputTokens        int                       `json:"output_tokens"`
	TotalTokens         int                       `json:"total_tokens"`
	InputTokensDetails  *azureUsageDetails        `json:"input_tokens_details,omitempty"`
	OutputTokensDetails *azureOutputTokensDetails `json:"output_tokens_details,omitempty"`
}

type azureIncompleteDetails struct {
	Reason string `json:"reason"`
}

type azureResponseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type azureResponseWire struct {
	ID                string                  `json:"id"`
	Status            string                  `json:"status"`
	IncompleteDetails *azureIncompleteDetails `json:"incomplete_details,omitempty"`
	Output            []azureItemWire         `json:"output,omitempty"`
	Usage             *azureUsageWire         `json:"usage,omitempty"`
	Error             *azureResponseError     `json:"error,omitempty"`
}

type azureSSEEvent struct {
	Type        string             `json:"type"`
	Response    *azureResponseWire `json:"response,omitempty"`
	OutputIndex int                `json:"output_index"`
	Item        *azureItemWire     `json:"item,omitempty"`
	Delta       string             `json:"delta,omitempty"`
	Arguments   string             `json:"arguments,omitempty"`
	Code        any                `json:"code,omitempty"`
	Message     string             `json:"message,omitempty"`
}

// azureSlot tracks one in-flight output item, keyed by the Responses API's
// output_index, so deltas route to the right position in partial.Content.
type azureSlot struct {
	kind        string // "thinking" | "text" | "toolCall"
	pos         int
	partialJSON string
}

func createAzureSlot(partial *msg.AssistantMessage, events chan<- msg.StreamEvent, slots map[int]*azureSlot, outputIndex int, item *azureItemWire) *azureSlot {
	switch item.Type {
	case "reasoning":
		partial.Content = append(partial.Content, msg.Thinking(""))
		pos := len(partial.Content) - 1
		slot := &azureSlot{kind: "thinking", pos: pos}
		slots[outputIndex] = slot
		events <- msg.StreamEvent{Type: msg.EventThinkingStart, ContentIndex: pos, Partial: partial}
		return slot
	case "message":
		partial.Content = append(partial.Content, msg.Text(""))
		pos := len(partial.Content) - 1
		slot := &azureSlot{kind: "text", pos: pos}
		slots[outputIndex] = slot
		events <- msg.StreamEvent{Type: msg.EventTextStart, ContentIndex: pos, Partial: partial}
		return slot
	case "function_call":
		id := item.CallID + "|" + item.ID
		partial.Content = append(partial.Content, msg.NewToolCall(id, item.Name, nil))
		pos := len(partial.Content) - 1
		slot := &azureSlot{kind: "toolCall", pos: pos, partialJSON: item.Arguments}
		slots[outputIndex] = slot
		events <- msg.StreamEvent{Type: msg.EventToolCallStart, ContentIndex: pos, Partial: partial}
		return slot
	default:
		// custom_tool_call, additional_tools, tool_search_* items: out of
		// scope this phase.
		return nil
	}
}

// mapAzureStopReason ports mapStopReason (openai-responses-shared.js).
func mapAzureStopReason(status, incompleteReason string) (msg.StopReason, string) {
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
		return msg.StopError, fmt.Sprintf("Unhandled stop reason: %s", status)
	}
}

// finalizeAzureResponse ports finalizeResponse (openai-responses-shared.js),
// including the Azure-specific reasoning-signature backfill: Azure OpenAI can
// omit reasoning.encrypted_content from response.output_item.done and
// provide it only in response.completed.response.output.
// reasoningPosByID maps a reasoning item's id to its position in
// partial.Content, populated when that reasoning block was finalized on
// output_item.done.
func finalizeAzureResponse(model provider.Model, partial *msg.AssistantMessage, resp *azureResponseWire, reasoningPosByID map[string]int) {
	for _, item := range resp.Output {
		if item.Type != "reasoning" || item.EncryptedContent == "" {
			continue
		}
		pos, ok := reasoningPosByID[item.ID]
		if !ok {
			continue
		}
		tc, ok := partial.Content[pos].(msg.ThinkingContent)
		if !ok || tc.ThinkingSignature == "" {
			continue
		}
		var stored map[string]any
		if json.Unmarshal([]byte(tc.ThinkingSignature), &stored) != nil {
			continue
		}
		if v, exists := stored["encrypted_content"]; exists {
			if s, ok := v.(string); ok && s != "" {
				continue
			}
		}
		stored["encrypted_content"] = item.EncryptedContent
		merged, err := json.Marshal(stored)
		if err != nil {
			continue
		}
		tc.ThinkingSignature = string(merged)
		partial.Content[pos] = tc
	}

	if resp.ID != "" {
		partial.ResponseID = resp.ID
	}

	if resp.Usage != nil {
		cached, cacheWrite := 0, 0
		if resp.Usage.InputTokensDetails != nil {
			cached = resp.Usage.InputTokensDetails.CachedTokens
			cacheWrite = resp.Usage.InputTokensDetails.CacheWriteTokens
		}
		input := resp.Usage.InputTokens - cached - cacheWrite
		if input < 0 {
			input = 0
		}
		partial.Usage.Input = input
		partial.Usage.Output = resp.Usage.OutputTokens
		partial.Usage.CacheRead = cached
		partial.Usage.CacheWrite = cacheWrite
		if resp.Usage.OutputTokensDetails != nil {
			r := resp.Usage.OutputTokensDetails.ReasoningTokens
			partial.Usage.Reasoning = &r
		}
		partial.Usage.TotalTokens = resp.Usage.TotalTokens
		computeAnthropicCost(model, &partial.Usage) // rate math is provider-agnostic
	}

	incompleteReason := ""
	if resp.IncompleteDetails != nil {
		incompleteReason = resp.IncompleteDetails.Reason
	}
	if incompleteReason != "" {
		partial.RawStopReason = resp.Status + "." + incompleteReason
	} else {
		partial.RawStopReason = resp.Status
	}
	reason, errMsg := mapAzureStopReason(resp.Status, incompleteReason)
	partial.StopReason = reason
	partial.ErrorMessage = errMsg

	hasToolCall := false
	for _, b := range partial.Content {
		if _, ok := b.(msg.ToolCall); ok {
			hasToolCall = true
			break
		}
	}
	if hasToolCall && partial.StopReason == msg.StopStop {
		partial.StopReason = msg.StopToolUse
	}
}

// Stream starts an Azure OpenAI Responses completion. The returned channel is
// closed once the stream ends; wait blocks for that and returns the final
// message or the terminating error.
func (c *AzureOpenAIResponsesClient) Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions, auth Auth) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
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

func (c *AzureOpenAIResponsesClient) run(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions, auth Auth, events chan<- msg.StreamEvent) (*msg.AssistantMessage, error) {
	partial := &msg.AssistantMessage{
		API:        string(provider.ApiAzureOpenAIResponses),
		Provider:   model.Provider,
		Model:      model.ID,
		Role:       msg.RoleAssistant,
		StopReason: msg.StopPending,
		Content:    msg.Blocks{},
	}

	wireReq := buildAzureRequest(model, transcript, opts)
	body, err := json.Marshal(wireReq)
	if err != nil {
		return errorOut(partial, events, false, err)
	}

	reqURL, err := azureResponsesURL(model)
	if err != nil {
		return errorOut(partial, events, false, err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return errorOut(partial, events, false, err)
	}
	httpReq.Header = azureHeaders(model, auth)

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

	slots := map[int]*azureSlot{}
	reasoningPosByID := map[string]int{}
	sawTerminal := false
	var streamFailure error

	r := bufio.NewReader(resp.Body)
	streamErr := scanSSE(r, func(ev sseEvent) {
		if streamFailure != nil {
			return
		}
		data := strings.TrimSpace(ev.Data)
		if data == "" {
			return
		}
		switch ev.Event {
		case "response.created":
			var e azureSSEEvent
			if json.Unmarshal([]byte(data), &e) == nil && e.Response != nil {
				partial.ResponseID = e.Response.ID
			}
		case "response.output_item.added":
			var e azureSSEEvent
			if json.Unmarshal([]byte(data), &e) != nil || e.Item == nil {
				return
			}
			createAzureSlot(partial, events, slots, e.OutputIndex, e.Item)
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			var e azureSSEEvent
			if json.Unmarshal([]byte(data), &e) != nil {
				return
			}
			slot := slots[e.OutputIndex]
			if slot == nil || slot.kind != "thinking" {
				return
			}
			tc := partial.Content[slot.pos].(msg.ThinkingContent)
			tc.Thinking += e.Delta
			partial.Content[slot.pos] = tc
			events <- msg.StreamEvent{Type: msg.EventThinkingDelta, ContentIndex: slot.pos, Delta: e.Delta, Partial: partial}
		case "response.reasoning_summary_part.done":
			var e azureSSEEvent
			if json.Unmarshal([]byte(data), &e) != nil {
				return
			}
			slot := slots[e.OutputIndex]
			if slot == nil || slot.kind != "thinking" {
				return
			}
			tc := partial.Content[slot.pos].(msg.ThinkingContent)
			tc.Thinking += "\n\n"
			partial.Content[slot.pos] = tc
			events <- msg.StreamEvent{Type: msg.EventThinkingDelta, ContentIndex: slot.pos, Delta: "\n\n", Partial: partial}
		case "response.output_text.delta", "response.refusal.delta":
			var e azureSSEEvent
			if json.Unmarshal([]byte(data), &e) != nil {
				return
			}
			slot := slots[e.OutputIndex]
			if slot == nil || slot.kind != "text" {
				return
			}
			tc := partial.Content[slot.pos].(msg.TextContent)
			tc.Text += e.Delta
			partial.Content[slot.pos] = tc
			events <- msg.StreamEvent{Type: msg.EventTextDelta, ContentIndex: slot.pos, Delta: e.Delta, Partial: partial}
		case "response.function_call_arguments.delta":
			var e azureSSEEvent
			if json.Unmarshal([]byte(data), &e) != nil {
				return
			}
			slot := slots[e.OutputIndex]
			if slot == nil || slot.kind != "toolCall" {
				return
			}
			slot.partialJSON += e.Delta
			tc := partial.Content[slot.pos].(msg.ToolCall)
			var args map[string]any
			if json.Unmarshal([]byte(slot.partialJSON), &args) == nil {
				tc.Arguments = args
			}
			partial.Content[slot.pos] = tc
			events <- msg.StreamEvent{Type: msg.EventToolCallDelta, ContentIndex: slot.pos, Delta: e.Delta, Partial: partial}
		case "response.function_call_arguments.done":
			var e azureSSEEvent
			if json.Unmarshal([]byte(data), &e) != nil {
				return
			}
			slot := slots[e.OutputIndex]
			if slot == nil || slot.kind != "toolCall" {
				return
			}
			prev := slot.partialJSON
			slot.partialJSON = e.Arguments
			tc := partial.Content[slot.pos].(msg.ToolCall)
			var args map[string]any
			if json.Unmarshal([]byte(slot.partialJSON), &args) == nil {
				tc.Arguments = args
			}
			partial.Content[slot.pos] = tc
			if strings.HasPrefix(e.Arguments, prev) {
				delta := e.Arguments[len(prev):]
				if delta != "" {
					events <- msg.StreamEvent{Type: msg.EventToolCallDelta, ContentIndex: slot.pos, Delta: delta, Partial: partial}
				}
			}
		case "response.output_item.done":
			var e azureSSEEvent
			if json.Unmarshal([]byte(data), &e) != nil || e.Item == nil {
				return
			}
			slot := slots[e.OutputIndex]
			if slot == nil {
				slot = createAzureSlot(partial, events, slots, e.OutputIndex, e.Item)
			}
			if slot == nil {
				return
			}
			switch e.Item.Type {
			case "reasoning":
				if slot.kind != "thinking" {
					return
				}
				var texts []string
				for _, s := range e.Item.Summary {
					texts = append(texts, s.Text)
				}
				summaryText := strings.Join(texts, "\n\n")
				var ctexts []string
				for _, cp := range e.Item.Content {
					ctexts = append(ctexts, cp.Text)
				}
				contentText := strings.Join(ctexts, "\n\n")
				tc := partial.Content[slot.pos].(msg.ThinkingContent)
				if summaryText != "" {
					tc.Thinking = summaryText
				} else if contentText != "" {
					tc.Thinking = contentText
				}
				sigBytes, _ := json.Marshal(e.Item)
				tc.ThinkingSignature = string(sigBytes)
				partial.Content[slot.pos] = tc
				reasoningPosByID[e.Item.ID] = slot.pos
				events <- msg.StreamEvent{Type: msg.EventThinkingEnd, ContentIndex: slot.pos, Content: tc.Thinking, Partial: partial}
				delete(slots, e.OutputIndex)
			case "message":
				if slot.kind != "text" {
					return
				}
				var texts []string
				for _, cp := range e.Item.Content {
					if cp.Type == "output_text" {
						texts = append(texts, cp.Text)
					} else {
						texts = append(texts, cp.Refusal)
					}
				}
				tc := partial.Content[slot.pos].(msg.TextContent)
				tc.Text = strings.Join(texts, "")
				partial.Content[slot.pos] = tc
				events <- msg.StreamEvent{Type: msg.EventTextEnd, ContentIndex: slot.pos, Content: tc.Text, Partial: partial}
				delete(slots, e.OutputIndex)
			case "function_call":
				if slot.kind != "toolCall" {
					return
				}
				raw := e.Item.Arguments
				if raw == "" {
					raw = slot.partialJSON
				}
				if raw == "" {
					raw = "{}"
				}
				var args map[string]any
				if json.Unmarshal([]byte(raw), &args) != nil {
					args = map[string]any{}
				}
				tc := partial.Content[slot.pos].(msg.ToolCall)
				tc.Arguments = args
				partial.Content[slot.pos] = tc
				events <- msg.StreamEvent{Type: msg.EventToolCallEnd, ContentIndex: slot.pos, ToolCall: &tc, Partial: partial}
				delete(slots, e.OutputIndex)
			}
		case "response.completed", "response.incomplete":
			var e azureSSEEvent
			if json.Unmarshal([]byte(data), &e) != nil || e.Response == nil {
				return
			}
			sawTerminal = true
			finalizeAzureResponse(model, partial, e.Response, reasoningPosByID)
		case "error":
			var e azureSSEEvent
			if json.Unmarshal([]byte(data), &e) != nil {
				streamFailure = fmt.Errorf("Error Code <unknown>: malformed error event")
				return
			}
			streamFailure = fmt.Errorf("Error Code %v: %s", e.Code, e.Message)
		case "response.failed":
			var e azureSSEEvent
			if json.Unmarshal([]byte(data), &e) != nil {
				//lint:ignore ST1005 wire-format fidelity: matches pi's literal "Unknown error (no error details in response)" (openai-responses-shared.js's response.failed handling)
				streamFailure = errors.New("Unknown error (no error details in response)")
				return
			}
			sawTerminal = true
			var errMsg string
			if e.Response != nil {
				partial.RawStopReason = e.Response.Status
			}
			switch {
			case e.Response != nil && e.Response.Error != nil:
				code := e.Response.Error.Code
				if code == "" {
					code = "unknown"
				}
				message := e.Response.Error.Message
				if message == "" {
					message = "no message"
				}
				errMsg = fmt.Sprintf("%s: %s", code, message)
			case e.Response != nil && e.Response.IncompleteDetails != nil && e.Response.IncompleteDetails.Reason != "":
				errMsg = "incomplete: " + e.Response.IncompleteDetails.Reason
			default:
				errMsg = "Unknown error (no error details in response)"
			}
			streamFailure = errors.New(errMsg)
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
	if streamFailure != nil {
		return errorOut(partial, events, ctx.Err() != nil, streamFailure)
	}
	if ctx.Err() != nil {
		return errorOut(partial, events, true, fmt.Errorf("request was aborted"))
	}
	if !sawTerminal {
		return errorOut(partial, events, false, fmt.Errorf("azure openai responses stream ended without a stop reason"))
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
