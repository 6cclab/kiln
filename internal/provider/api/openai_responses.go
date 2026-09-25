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
	"strconv"
	"strings"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// OpenAI Responses API streaming client, ported from pi-ai's
// dist/api/openai-responses.js (request construction, buildParams) and
// dist/api/openai-responses-shared.js (message/tool conversion and the SSE
// state machine, processResponsesStream). Every deviation from pi's
// behavior is called out inline where it happens; see also the phase report
// handed back by the porting agent for the full list:
//
//   - github-copilot's dynamic headers/vision special-casing (openai-responses.js
//     createClient) is out of scope; this client behaves like every other
//     openai-responses provider.
//   - service_tier / applyServiceTierPricing (openai-responses.js buildParams,
//     getServiceTierCostMultiplier) is out of scope: provider.StreamOptions has
//     no service tier field.
//   - prompt_cache_key / prompt_cache_retention / prompt_cache_options
//     (session affinity, cacheRetention) are out of scope: provider.StreamOptions
//     has no sessionId/cacheRetention concept this phase.
//   - tool_choice is out of scope: provider.StreamOptions has no toolChoice field.
//   - custom/grammar tools, additional_tools, tool_search (openai-responses-shared.js
//     convertResponsesTools "custom" branch, appendSystemToolAdditions) are out of
//     scope: they require compat.supportsOpenAIGrammarTools/supportsAdditionalTools/
//     supportsToolSearch, all default false, so the in-scope function-tool-only path
//     is what fires by default anyway.
//   - mid-conversation system message folding (resolveTranscript /
//     renderSystemMessageUpdate) is out of scope: this harness's transcript is a
//     flat []msg.Message with no such resolution step, matching how
//     anthropic_messages.go and openai_completions.go already handle it (pick the
//     first msg.SystemMessage, override with opts.SystemPrompt).
//   - assistant text block id/signature replay fidelity (parseTextSignature /
//     encodeTextSignatureV1) is simplified to a stable synthesized id
//     ("msg_pi_<index>"); a thinking block with no ThinkingSignature is dropped
//     from the replayed request rather than synthesizing one, matching what pi
//     does when a caller has no signature to replay.
//   - applyMessagePhaseStopReason (item.phase === "final_answer") is skipped: this
//     harness's content blocks carry no `phase` field, and the msg package is out
//     of scope for this change.
//   - custom_tool_call / custom_tool_call_output (grammar/constrained-sampling tool
//     calls) are out of scope, matching the custom/grammar tools deviation above.

// OpenAIResponsesClient streams completions against one OpenAI
// Responses-shaped endpoint (model.BaseURL + "/responses").
type OpenAIResponsesClient struct {
	HTTPClient *http.Client
}

func (c *OpenAIResponsesClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// --- wire request shapes ---

type responsesReasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary,omitempty"`
}

type responsesFunctionTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      *bool           `json:"strict,omitempty"`
}

type responsesRequest struct {
	Model           string                  `json:"model"`
	Input           []json.RawMessage       `json:"input"`
	Stream          bool                    `json:"stream"`
	Store           bool                    `json:"store"`
	MaxOutputTokens int                     `json:"max_output_tokens,omitempty"`
	Temperature     *float64                `json:"temperature,omitempty"`
	Tools           []responsesFunctionTool `json:"tools,omitempty"`
	Reasoning       *responsesReasoning     `json:"reasoning,omitempty"`
	Include         []string                `json:"include,omitempty"`
}

// --- wire input-item shapes (openai-responses-shared.js convertResponsesMessages) ---

type responsesInstructionsItem struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// responsesInputPart is a user-message content part or tool-result output
// part: {type:"input_text",text} | {type:"input_image",detail,image_url}.
type responsesInputPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Detail   string `json:"detail,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type responsesUserMessageItem struct {
	Role    string               `json:"role"`
	Content []responsesInputPart `json:"content"`
}

type responsesOutputTextPart struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Annotations []any  `json:"annotations"`
}

type responsesAssistantMessageItem struct {
	Type    string                    `json:"type"`
	Role    string                    `json:"role"`
	Content []responsesOutputTextPart `json:"content"`
	Status  string                    `json:"status"`
	ID      string                    `json:"id"`
}

type responsesFunctionCallItem struct {
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responsesFunctionCallOutputItem struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output any    `json:"output"`
}

func marshalResponsesItem(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// splitToolCallID (pi's convention: ToolCall.ID is "<call_id>|<item_id>") is
// defined once for the package in azure_openai_responses.go; reused here
// rather than redeclared.

// buildOpenAIResponsesRequest builds the request body pi's buildParams
// (openai-responses.js) would send for the in-scope subset described in the
// file header.
func buildOpenAIResponsesRequest(model provider.Model, transcript []msg.Message, opts provider.StreamOptions) responsesRequest {
	compat := model.OpenAIResponsesCompat()

	req := responsesRequest{Model: model.ID, Stream: true, Store: false}

	// openai-responses.js buildParams: max_output_tokens, clamped to
	// OPENAI_RESPONSES_MIN_OUTPUT_TOKENS (16), only when compat allows it.
	maxTokens := model.MaxTokens
	if opts.MaxTokens > 0 {
		maxTokens = opts.MaxTokens
	}
	if maxTokens > 0 && boolDefault(compat.SupportsMaxOutputTokens, true) {
		if maxTokens < 16 {
			maxTokens = 16
		}
		req.MaxOutputTokens = maxTokens
	}
	if opts.Temperature != nil {
		req.Temperature = opts.Temperature
	}

	// openai-responses-shared.js convertResponsesMessages:
	// `model.reasoning && compat?.supportsDeveloperRole !== false` -> "developer".
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
	if systemText != "" {
		req.Input = append(req.Input, marshalResponsesItem(responsesInstructionsItem{Role: instructionRole, Content: systemText}))
	}

	// msgIndex mirrors pi's msgIndex: every transcript message increments it
	// except a leading system message (which was already folded above).
	msgIndex := 0
	for i, m := range transcript {
		_, isSystem := m.(msg.SystemMessage)
		isLeadingSystem := i == 0 && isSystem

		switch t := m.(type) {
		case msg.SystemMessage:
			// Folded into the instructions item above; mid-conversation
			// system messages are a deviation, see file header.
		case msg.UserMessage:
			parts := convertResponsesUserContent(t.Content)
			if len(parts) > 0 {
				req.Input = append(req.Input, marshalResponsesItem(responsesUserMessageItem{Role: "user", Content: parts}))
			}
		case msg.AssistantMessage:
			req.Input = append(req.Input, convertResponsesAssistant(t, msgIndex)...)
		case msg.ToolResultMessage:
			req.Input = append(req.Input, convertResponsesToolResult(model, t))
		}

		if !isLeadingSystem {
			msgIndex++
		}
	}

	if len(opts.Tools) > 0 {
		req.Tools = convertResponsesTools(model, opts.Tools, compat)
	}

	if model.Reasoning {
		if opts.ThinkingLevel != "" && opts.ThinkingLevel != provider.ThinkingOff {
			// openai-responses.js buildParams: effort falls back to
			// model.thinkingLevelMap[level] ?? level; summary is always
			// "auto" since this harness has no separate reasoningSummary
			// option.
			effort := string(opts.ThinkingLevel)
			if model.ThinkingLevelMap != nil {
				if mapped, ok := model.ThinkingLevelMap[opts.ThinkingLevel]; ok && mapped != nil {
					effort = *mapped
				}
			}
			req.Reasoning = &responsesReasoning{Effort: effort, Summary: "auto"}
			req.Include = []string{"reasoning.encrypted_content"}
		} else {
			// openai-responses.js: `model.thinkingLevelMap?.off !== null` ->
			// send `{effort: model.thinkingLevelMap?.off ?? "none"}`; a
			// present key mapped to nil (explicit JS null) means "send no
			// reasoning field at all".
			explicitOffNull := false
			offEffort := "none"
			if model.ThinkingLevelMap != nil {
				if mapped, ok := model.ThinkingLevelMap[provider.ThinkingOff]; ok {
					if mapped == nil {
						explicitOffNull = true
					} else {
						offEffort = *mapped
					}
				}
			}
			if !explicitOffNull {
				req.Reasoning = &responsesReasoning{Effort: offEffort}
			}
		}
	}

	return req
}

func convertResponsesUserContent(blocks msg.Blocks) []responsesInputPart {
	var parts []responsesInputPart
	for _, b := range blocks {
		switch c := b.(type) {
		case msg.TextContent:
			parts = append(parts, responsesInputPart{Type: "input_text", Text: c.Text})
		case msg.ImageContent:
			parts = append(parts, responsesInputPart{Type: "input_image", Detail: "auto", ImageURL: "data:" + c.MimeType + ";base64," + c.Data})
		}
	}
	return parts
}

// convertResponsesAssistant ports the assistant-message branch of
// convertResponsesMessages (openai-responses-shared.js:169-244) for the
// in-scope block kinds (text, thinking-with-signature, toolCall).
func convertResponsesAssistant(t msg.AssistantMessage, msgIndex int) []json.RawMessage {
	var out []json.RawMessage
	textBlockIndex := 0
	for _, b := range t.Content {
		switch c := b.(type) {
		case msg.ThinkingContent:
			// openai-responses-shared.js:178-181: only replayed when a
			// signature (the raw reasoning item, JSON-encoded) is present;
			// otherwise the block is dropped. See file header deviation.
			if c.ThinkingSignature != "" && json.Valid([]byte(c.ThinkingSignature)) {
				out = append(out, json.RawMessage(c.ThinkingSignature))
			}
		case msg.TextContent:
			id := "msg_pi_" + strconv.Itoa(msgIndex)
			if textBlockIndex > 0 {
				id = "msg_pi_" + strconv.Itoa(msgIndex) + "_" + strconv.Itoa(textBlockIndex)
			}
			textBlockIndex++
			out = append(out, marshalResponsesItem(responsesAssistantMessageItem{
				Type:    "message",
				Role:    "assistant",
				Content: []responsesOutputTextPart{{Type: "output_text", Text: c.Text, Annotations: []any{}}},
				Status:  "completed",
				ID:      id,
			}))
		case msg.ToolCall:
			callID, itemID := splitToolCallID(c.ID)
			args, _ := json.Marshal(c.Arguments)
			out = append(out, marshalResponsesItem(responsesFunctionCallItem{
				Type:      "function_call",
				ID:        itemID,
				CallID:    callID,
				Name:      c.Name,
				Arguments: string(args),
			}))
		}
	}
	return out
}

// convertResponsesToolResult ports the toolResult branch of
// convertResponsesMessages plus convertToolResultOutput
// (openai-responses-shared.js:37-59, 245-262), custom-tool-call output
// excluded (out of scope, see file header).
func convertResponsesToolResult(model provider.Model, t msg.ToolResultMessage) json.RawMessage {
	callID, _ := splitToolCallID(t.ToolCallID)
	output := convertResponsesToolResultOutput(model, t.Content)
	return marshalResponsesItem(responsesFunctionCallOutputItem{Type: "function_call_output", CallID: callID, Output: output})
}

func convertResponsesToolResultOutput(model provider.Model, blocks msg.Blocks) any {
	var textParts []string
	var images []msg.ImageContent
	for _, b := range blocks {
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

	var out []responsesInputPart
	if hasText {
		out = append(out, responsesInputPart{Type: "input_text", Text: text})
	}
	for _, img := range images {
		out = append(out, responsesInputPart{Type: "input_image", Detail: "auto", ImageURL: "data:" + img.MimeType + ";base64," + img.Data})
	}
	return out
}

// convertResponsesTools ports convertResponsesTools' in-scope subset
// (openai-responses-shared.js:271-304): function tools only, no
// custom/grammar tools (compat.supportsOpenAIGrammarTools default false).
func convertResponsesTools(model provider.Model, tools []provider.ToolDef, compat provider.OpenAIResponsesCompat) []responsesFunctionTool {
	supportsStrict := boolDefault(compat.SupportsStrictMode, false)
	out := make([]responsesFunctionTool, 0, len(tools))
	for _, td := range tools {
		schema := td.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		tool := responsesFunctionTool{Type: "function", Name: td.Name, Description: td.Description, Parameters: schema}
		if supportsStrict {
			strict := model.SupportsStrictMode()
			tool.Strict = &strict
		}
		out = append(out, tool)
	}
	return out
}

// --- response wire shapes (processResponsesStream) ---

type responsesEventProbe struct {
	Type string `json:"type"`
}

type responsesCreatedWire struct {
	Response struct {
		ID string `json:"id"`
	} `json:"response"`
}

type responsesItemWire struct {
	Type      string                 `json:"type"`
	ID        string                 `json:"id"`
	CallID    string                 `json:"call_id"`
	Name      string                 `json:"name"`
	Arguments string                 `json:"arguments"`
	Summary   []responsesSummaryWire `json:"summary"`
	Content   []responsesContentWire `json:"content"`
}

type responsesSummaryWire struct {
	Text string `json:"text"`
}

type responsesContentWire struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

type responsesOutputItemWire struct {
	OutputIndex int             `json:"output_index"`
	Item        json.RawMessage `json:"item"`
}

type responsesDeltaWire struct {
	OutputIndex int    `json:"output_index"`
	Delta       string `json:"delta"`
}

type responsesFuncArgsDoneWire struct {
	OutputIndex int    `json:"output_index"`
	Arguments   string `json:"arguments"`
}

type responsesErrorWire struct {
	Code    any    `json:"code"`
	Message string `json:"message"`
}

type responsesUsageWire struct {
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

type responsesResponseWire struct {
	ID                string               `json:"id"`
	Status            string               `json:"status"`
	Usage             *responsesUsageWire  `json:"usage"`
	IncompleteDetails *responsesIncomplete `json:"incomplete_details"`
	Error             *responsesErrorField `json:"error"`
}

type responsesIncomplete struct {
	Reason string `json:"reason"`
}

type responsesErrorField struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type responsesTerminalWire struct {
	Response responsesResponseWire `json:"response"`
}

// responsesSlot tracks one in-flight output_index's content block, mirroring
// pi's outputSlots map.
type responsesSlot struct {
	kind         string // "thinking" | "text" | "toolCall"
	contentIndex int
	partialJSON  string
}

// mapResponsesStopReason is pi's mapStopReason
// (openai-responses-shared.js), quoted verbatim in the type's doc comment:
//
//	function mapStopReason(status, incompleteReason) {
//	    if (!status) return { stopReason: "stop" };
//	    switch (status) {
//	        case "completed": return { stopReason: "stop" };
//	        case "incomplete":
//	            if (incompleteReason === "max_output_tokens") return { stopReason: "length" };
//	            return { stopReason: "error", errorMessage: incompleteReason ? `Response incomplete: ${incompleteReason}` : "Response incomplete without a provider reason" };
//	        case "failed": case "cancelled": return { stopReason: "error" };
//	        case "in_progress": case "queued": return { stopReason: "stop" };
//	        default: throw new Error(`Unhandled stop reason: ${status}`);
//	    }
//	}
func mapResponsesStopReason(status, incompleteReason string) (msg.StopReason, string) {
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

func openAIResponsesHeaders(model provider.Model, auth Auth) http.Header {
	// Same merge pattern as openAIHeaders in openai_completions.go: default
	// Content-Type/Authorization, then model.Headers, then auth.Headers.
	return openAIHeaders(model, auth)
}

// Stream starts an OpenAI Responses completion. The returned channel is
// closed once the stream ends; wait blocks for that and returns the final
// message or the terminating error.
func (c *OpenAIResponsesClient) Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions, auth Auth) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
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

func (c *OpenAIResponsesClient) run(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions, auth Auth, events chan<- msg.StreamEvent) (*msg.AssistantMessage, error) {
	partial := &msg.AssistantMessage{
		API:        string(provider.ApiOpenAIResponses),
		Provider:   model.Provider,
		Model:      model.ID,
		Role:       msg.RoleAssistant,
		StopReason: msg.StopPending,
		Content:    msg.Blocks{},
	}

	wireReq := buildOpenAIResponsesRequest(model, transcript, opts)
	body, err := json.Marshal(wireReq)
	if err != nil {
		return errorOut(partial, events, false, err)
	}

	url := strings.TrimRight(model.BaseURL, "/") + "/responses"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return errorOut(partial, events, false, err)
	}
	httpReq.Header = openAIResponsesHeaders(model, auth)

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

	slots := map[int]*responsesSlot{}
	sawTerminal := false

	createSlot := func(outputIndex int, item responsesItemWire) *responsesSlot {
		switch item.Type {
		case "reasoning":
			partial.Content = append(partial.Content, msg.Thinking(""))
			pos := len(partial.Content) - 1
			slot := &responsesSlot{kind: "thinking", contentIndex: pos}
			slots[outputIndex] = slot
			events <- msg.StreamEvent{Type: msg.EventThinkingStart, ContentIndex: pos, Partial: partial}
			return slot
		case "message":
			partial.Content = append(partial.Content, msg.Text(""))
			pos := len(partial.Content) - 1
			slot := &responsesSlot{kind: "text", contentIndex: pos}
			slots[outputIndex] = slot
			events <- msg.StreamEvent{Type: msg.EventTextStart, ContentIndex: pos, Partial: partial}
			return slot
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
			slot := &responsesSlot{kind: "toolCall", contentIndex: pos, partialJSON: item.Arguments}
			slots[outputIndex] = slot
			events <- msg.StreamEvent{Type: msg.EventToolCallStart, ContentIndex: pos, Partial: partial}
			return slot
			// custom_tool_call is out of scope (grammar tools); see file header.
		}
		return nil
	}

	getSlot := func(outputIndex int, kind string) *responsesSlot {
		s, ok := slots[outputIndex]
		if !ok || s.kind != kind {
			return nil
		}
		return s
	}

	finalizeResponse := func(resp responsesResponseWire) {
		if resp.ID != "" {
			partial.ResponseID = resp.ID
		}
		if resp.Usage != nil {
			u := resp.Usage
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
		}
		// computeAnthropicCost is provider-agnostic rate math (see
		// openai_completions.go's reuse of it); reasoning tokens are
		// informational only here, not separately priced.
		computeAnthropicCost(model, &partial.Usage)

		incompleteReason := ""
		if resp.IncompleteDetails != nil {
			incompleteReason = resp.IncompleteDetails.Reason
		}
		if incompleteReason != "" {
			partial.RawStopReason = resp.Status + "." + incompleteReason
		} else {
			partial.RawStopReason = resp.Status
		}
		reason, errMsg := mapResponsesStopReason(resp.Status, incompleteReason)
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

	r := bufio.NewReader(resp.Body)
	streamErr := scanSSE(r, func(ev sseEvent) {
		if sawTerminal {
			return
		}
		data := strings.TrimSpace(ev.Data)
		if data == "" {
			return
		}
		var probe responsesEventProbe
		if json.Unmarshal([]byte(data), &probe) != nil {
			return
		}
		switch probe.Type {
		case "response.created":
			var w responsesCreatedWire
			if json.Unmarshal([]byte(data), &w) == nil {
				partial.ResponseID = w.Response.ID
			}
		case "response.output_item.added":
			var w responsesOutputItemWire
			if json.Unmarshal([]byte(data), &w) != nil {
				return
			}
			var item responsesItemWire
			if json.Unmarshal(w.Item, &item) != nil {
				return
			}
			createSlot(w.OutputIndex, item)
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			// Both feed the same thinking accumulator: pi treats
			// summary-text and full reasoning-text deltas identically for a
			// thinking-typed slot (openai-responses-shared.js:484-519).
			var w responsesDeltaWire
			if json.Unmarshal([]byte(data), &w) != nil {
				return
			}
			slot := getSlot(w.OutputIndex, "thinking")
			if slot == nil {
				return
			}
			tc := partial.Content[slot.contentIndex].(msg.ThinkingContent)
			tc.Thinking += w.Delta
			partial.Content[slot.contentIndex] = tc
			events <- msg.StreamEvent{Type: msg.EventThinkingDelta, ContentIndex: slot.contentIndex, Delta: w.Delta, Partial: partial}
		case "response.reasoning_summary_part.done":
			var w responsesDeltaWire
			_ = json.Unmarshal([]byte(data), &w)
			slot := getSlot(w.OutputIndex, "thinking")
			if slot == nil {
				return
			}
			tc := partial.Content[slot.contentIndex].(msg.ThinkingContent)
			tc.Thinking += "\n\n"
			partial.Content[slot.contentIndex] = tc
			events <- msg.StreamEvent{Type: msg.EventThinkingDelta, ContentIndex: slot.contentIndex, Delta: "\n\n", Partial: partial}
		case "response.output_text.delta", "response.refusal.delta":
			var w responsesDeltaWire
			if json.Unmarshal([]byte(data), &w) != nil {
				return
			}
			slot := getSlot(w.OutputIndex, "text")
			if slot == nil {
				return
			}
			tc := partial.Content[slot.contentIndex].(msg.TextContent)
			tc.Text += w.Delta
			partial.Content[slot.contentIndex] = tc
			events <- msg.StreamEvent{Type: msg.EventTextDelta, ContentIndex: slot.contentIndex, Delta: w.Delta, Partial: partial}
		case "response.function_call_arguments.delta":
			var w responsesDeltaWire
			if json.Unmarshal([]byte(data), &w) != nil {
				return
			}
			slot := getSlot(w.OutputIndex, "toolCall")
			if slot == nil {
				return
			}
			slot.partialJSON += w.Delta
			var args map[string]any
			if json.Unmarshal([]byte(slot.partialJSON), &args) == nil {
				tc := partial.Content[slot.contentIndex].(msg.ToolCall)
				tc.Arguments = args
				partial.Content[slot.contentIndex] = tc
			}
			events <- msg.StreamEvent{Type: msg.EventToolCallDelta, ContentIndex: slot.contentIndex, Delta: w.Delta, Partial: partial}
		case "response.function_call_arguments.done":
			var w responsesFuncArgsDoneWire
			if json.Unmarshal([]byte(data), &w) != nil {
				return
			}
			slot := getSlot(w.OutputIndex, "toolCall")
			if slot == nil {
				return
			}
			previous := slot.partialJSON
			slot.partialJSON = w.Arguments
			var args map[string]any
			if json.Unmarshal([]byte(slot.partialJSON), &args) == nil {
				tc := partial.Content[slot.contentIndex].(msg.ToolCall)
				tc.Arguments = args
				partial.Content[slot.contentIndex] = tc
			}
			if strings.HasPrefix(w.Arguments, previous) {
				delta := w.Arguments[len(previous):]
				if delta != "" {
					events <- msg.StreamEvent{Type: msg.EventToolCallDelta, ContentIndex: slot.contentIndex, Delta: delta, Partial: partial}
				}
			}
		case "response.output_item.done":
			var w responsesOutputItemWire
			if json.Unmarshal([]byte(data), &w) != nil {
				return
			}
			var item responsesItemWire
			if json.Unmarshal(w.Item, &item) != nil {
				return
			}
			slot, ok := slots[w.OutputIndex]
			if !ok {
				slot = createSlot(w.OutputIndex, item)
			}
			if slot == nil {
				return
			}
			switch item.Type {
			case "reasoning":
				if slot.kind != "thinking" {
					return
				}
				var summaryTexts, contentTexts []string
				for _, s := range item.Summary {
					summaryTexts = append(summaryTexts, s.Text)
				}
				for _, cp := range item.Content {
					contentTexts = append(contentTexts, cp.Text)
				}
				tc := partial.Content[slot.contentIndex].(msg.ThinkingContent)
				if len(summaryTexts) > 0 {
					tc.Thinking = strings.Join(summaryTexts, "\n\n")
				} else if len(contentTexts) > 0 {
					tc.Thinking = strings.Join(contentTexts, "\n\n")
				}
				tc.ThinkingSignature = string(w.Item)
				partial.Content[slot.contentIndex] = tc
				events <- msg.StreamEvent{Type: msg.EventThinkingEnd, ContentIndex: slot.contentIndex, Content: tc.Thinking, Partial: partial}
				delete(slots, w.OutputIndex)
			case "message":
				if slot.kind != "text" {
					return
				}
				var b strings.Builder
				for _, cp := range item.Content {
					if cp.Type == "output_text" {
						b.WriteString(cp.Text)
					} else {
						b.WriteString(cp.Refusal)
					}
				}
				tc := partial.Content[slot.contentIndex].(msg.TextContent)
				tc.Text = b.String()
				partial.Content[slot.contentIndex] = tc
				events <- msg.StreamEvent{Type: msg.EventTextEnd, ContentIndex: slot.contentIndex, Content: tc.Text, Partial: partial}
				delete(slots, w.OutputIndex)
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
				var args map[string]any
				_ = json.Unmarshal([]byte(finalArgs), &args)
				tc := partial.Content[slot.contentIndex].(msg.ToolCall)
				tc.Arguments = args
				partial.Content[slot.contentIndex] = tc
				events <- msg.StreamEvent{Type: msg.EventToolCallEnd, ContentIndex: slot.contentIndex, ToolCall: &tc, Partial: partial}
				delete(slots, w.OutputIndex)
				// custom_tool_call finalization is out of scope (grammar tools).
			}
		case "response.completed", "response.incomplete":
			var w responsesTerminalWire
			if json.Unmarshal([]byte(data), &w) != nil {
				return
			}
			sawTerminal = true
			finalizeResponse(w.Response)
		case "error":
			var w responsesErrorWire
			_ = json.Unmarshal([]byte(data), &w)
			sawTerminal = true
			partial.StopReason = msg.StopError
			partial.ErrorMessage = fmt.Sprintf("Error Code %v: %s", w.Code, w.Message)
		case "response.failed":
			var w responsesTerminalWire
			_ = json.Unmarshal([]byte(data), &w)
			sawTerminal = true
			partial.RawStopReason = w.Response.Status
			var errMsg string
			switch {
			case w.Response.Error != nil:
				code := w.Response.Error.Code
				if code == "" {
					code = "unknown"
				}
				message := w.Response.Error.Message
				if message == "" {
					message = "no message"
				}
				errMsg = code + ": " + message
			case w.Response.IncompleteDetails != nil && w.Response.IncompleteDetails.Reason != "":
				errMsg = "incomplete: " + w.Response.IncompleteDetails.Reason
			default:
				errMsg = "Unknown error (no error details in response)"
			}
			partial.ErrorMessage = errMsg
			partial.StopReason = msg.StopError
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
	if !sawTerminal {
		return errorOut(partial, events, false, fmt.Errorf("OpenAI Responses stream ended before a terminal response event"))
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
