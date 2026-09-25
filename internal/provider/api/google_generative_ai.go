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
	"regexp"
	"strings"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// Google Generative AI (Gemini) streaming client, ported from pi-ai's
// dist/api/google-generative-ai.js and the shared helpers in
// dist/api/google-shared.js. pi drives this API through the @google/genai
// SDK's `client.models.generateContentStream`, which is documented (and
// observed via the SDK's own request builder) to POST to
// "{baseUrl}/models/{model}:streamGenerateContent?alt=sse" with an
// `x-goog-api-key` header and receive `alt=sse` Server-Sent Events whose
// `data:` payload is one GenerateContentResponse JSON object per chunk; this
// client speaks that wire shape directly rather than depending on the SDK.
//
// Deviations from pi-ai's google-shared.js convertMessages, noted here since
// there is no single line to quote per omission:
//   - Cloud Code Assist's "merge consecutive functionResponse into the last
//     user turn" behavior (convertMessages ~line 279) IS implemented (see
//     appendOrMergeFunctionResponse below).
//   - The `useParameters`/`parametersJsonSchema` OpenAPI-schema-stripping
//     path (sanitizeForOpenApi) is NOT implemented; tool parameter schemas
//     are passed through as parametersJsonSchema verbatim, since this phase
//     has no Claude-via-Cloud-Code-Assist model to require it.
//   - resolveGoogleFunctionCallingMode / FunctionCallingConfigMode
//     (VALIDATED/strict-mode tool sampling) is NOT implemented; toolConfig is
//     omitted and Gemini's default AUTO mode applies.
//   - getGoogleBudget's per-model-family budget tables (2.5-pro/-flash/
//     -flash-lite) and usesGoogleThinkingLevel's Gemini-3/Gemma-4 regex are
//     ported faithfully (see budgetForGoogleThinkingLevel / usesGoogleThinkingLevel).
type GoogleGenerativeAIClient struct {
	HTTPClient *http.Client
}

func (c *GoogleGenerativeAIClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// --- wire request shapes ---

type googlePart struct {
	Text             string              `json:"text,omitempty"`
	Thought          bool                `json:"thought,omitempty"`
	ThoughtSignature string              `json:"thoughtSignature,omitempty"`
	InlineData       *googleInlineData   `json:"inlineData,omitempty"`
	FunctionCall     *googleFunctionCall `json:"functionCall,omitempty"`
	FunctionResponse *googleFunctionResp `json:"functionResponse,omitempty"`
}

type googleInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type googleFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
	ID   string         `json:"id,omitempty"`
}

type googleFunctionResp struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
	Parts    []googlePart   `json:"parts,omitempty"`
	ID       string         `json:"id,omitempty"`
}

type googleContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []googlePart `json:"parts"`
}

type googleSystemInstruction struct {
	Parts []googlePart `json:"parts"`
}

type googleFunctionDeclaration struct {
	Name                 string          `json:"name"`
	Description          string          `json:"description,omitempty"`
	ParametersJsonSchema json.RawMessage `json:"parametersJsonSchema,omitempty"`
}

type googleTool struct {
	FunctionDeclarations []googleFunctionDeclaration `json:"functionDeclarations"`
}

type googleThinkingConfig struct {
	ThinkingBudget  *int   `json:"thinkingBudget,omitempty"`
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
	IncludeThoughts bool   `json:"includeThoughts,omitempty"`
}

type googleGenerationConfig struct {
	Temperature     *float64              `json:"temperature,omitempty"`
	MaxOutputTokens int                   `json:"maxOutputTokens,omitempty"`
	ThinkingConfig  *googleThinkingConfig `json:"thinkingConfig,omitempty"`
}

type googleRequest struct {
	Contents          []googleContent          `json:"contents"`
	SystemInstruction *googleSystemInstruction `json:"systemInstruction,omitempty"`
	Tools             []googleTool             `json:"tools,omitempty"`
	GenerationConfig  *googleGenerationConfig  `json:"generationConfig,omitempty"`
}

// --- request construction ---

// requiresGoogleToolCallID mirrors google-shared.js's requiresToolCallId:
// Claude/gpt-oss models behind Cloud Code Assist and Gemini 3+ require an
// explicit `id` field on functionCall/functionResponse.
func requiresGoogleToolCallID(modelID string) bool {
	lower := strings.ToLower(modelID)
	if strings.HasPrefix(lower, "claude-") || strings.HasPrefix(lower, "gpt-oss-") {
		return true
	}
	if v, ok := geminiMajorVersion(lower); ok && v >= 3 {
		return true
	}
	return false
}

var geminiVersionRe = regexp.MustCompile(`^gemini(?:-live)?-(\d+)`)

func geminiMajorVersion(lowerModelID string) (int, bool) {
	m := geminiVersionRe.FindStringSubmatch(lowerModelID)
	if m == nil {
		return 0, false
	}
	var v int
	for _, ch := range m[1] {
		v = v*10 + int(ch-'0')
	}
	return v, true
}

// gemini3PlusRe matches google-shared.js's usesGoogleThinkingLevel regex:
// /gemini-3(?:\.\d+)?-(?:pro|flash)/ plus the gemma-4/gemma4 pattern.
var (
	gemini3ThinkingRe = regexp.MustCompile(`gemini-3(?:\.\d+)?-(?:pro|flash)`)
	gemma4Re          = regexp.MustCompile(`gemma-?4`)
)

// usesGoogleThinkingLevel reports whether model uses Gemini's discrete
// thinkingLevel control (MINIMAL/LOW/MEDIUM/HIGH) instead of thinkingBudget.
func usesGoogleThinkingLevel(modelID string) bool {
	lower := strings.ToLower(modelID)
	return gemini3ThinkingRe.MatchString(lower) ||
		lower == "gemini-flash-latest" ||
		lower == "gemini-flash-lite-latest" ||
		gemma4Re.MatchString(lower)
}

// toGoogleThinkingLevel maps a pi ThinkingLevel to Gemini's discrete level
// string, per google-shared.js's toGoogleThinkingLevel. xhigh/max collapse
// to HIGH, the highest level Gemini exposes.
func toGoogleThinkingLevel(level provider.ThinkingLevel) string {
	switch level {
	case provider.ThinkingMinimal:
		return "MINIMAL"
	case provider.ThinkingLow:
		return "LOW"
	case provider.ThinkingMedium:
		return "MEDIUM"
	case provider.ThinkingHigh, provider.ThinkingXHigh, provider.ThinkingMax:
		return "HIGH"
	default:
		return ""
	}
}

// budgetForGoogleThinkingLevel ports getGoogleBudget's per-model-family
// tables (google-generative-ai.js:336-368 / google-vertex.js:406-429).
// Returns -1 (Gemini's "dynamic/auto" sentinel) for model families pi has no
// table for.
func budgetForGoogleThinkingLevel(modelID string, level provider.ThinkingLevel) int {
	var table map[provider.ThinkingLevel]int
	switch {
	case strings.Contains(modelID, "2.5-pro"):
		table = map[provider.ThinkingLevel]int{provider.ThinkingMinimal: 128, provider.ThinkingLow: 2048, provider.ThinkingMedium: 8192, provider.ThinkingHigh: 32768}
	case strings.Contains(modelID, "2.5-flash-lite"):
		table = map[provider.ThinkingLevel]int{provider.ThinkingMinimal: 512, provider.ThinkingLow: 2048, provider.ThinkingMedium: 8192, provider.ThinkingHigh: 24576}
	case strings.Contains(modelID, "2.5-flash"):
		table = map[provider.ThinkingLevel]int{provider.ThinkingMinimal: 128, provider.ThinkingLow: 2048, provider.ThinkingMedium: 8192, provider.ThinkingHigh: 24576}
	default:
		return -1
	}
	if v, ok := table[level]; ok {
		return v
	}
	if level == provider.ThinkingXHigh || level == provider.ThinkingMax {
		return table[provider.ThinkingHigh]
	}
	return -1
}

func buildGoogleRequest(model provider.Model, transcript []msg.Message, opts provider.StreamOptions) googleRequest {
	req := googleRequest{}

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
		req.SystemInstruction = &googleSystemInstruction{Parts: []googlePart{{Text: systemText}}}
	}

	req.Contents = convertGoogleMessages(model, transcript)

	if len(opts.Tools) > 0 {
		decls := make([]googleFunctionDeclaration, 0, len(opts.Tools))
		for _, td := range opts.Tools {
			schema := td.Parameters
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			decls = append(decls, googleFunctionDeclaration{Name: td.Name, Description: td.Description, ParametersJsonSchema: schema})
		}
		req.Tools = []googleTool{{FunctionDeclarations: decls}}
	}

	gc := &googleGenerationConfig{}
	if opts.Temperature != nil {
		gc.Temperature = opts.Temperature
	}
	maxTokens := model.MaxTokens
	if opts.MaxTokens > 0 {
		maxTokens = opts.MaxTokens
	}
	if maxTokens > 0 {
		gc.MaxOutputTokens = maxTokens
	}

	if model.Reasoning && opts.ThinkingLevel != "" && opts.ThinkingLevel != provider.ThinkingOff {
		tc := &googleThinkingConfig{IncludeThoughts: true}
		level := opts.ThinkingLevel
		if model.ThinkingLevelMap != nil {
			if mapped, ok := model.ThinkingLevelMap[level]; ok && mapped != nil {
				level = provider.ThinkingLevel(strings.ToLower(*mapped))
			}
		}
		if usesGoogleThinkingLevel(model.ID) {
			tc.ThinkingLevel = toGoogleThinkingLevel(level)
		} else {
			budget := budgetForGoogleThinkingLevel(model.ID, level)
			tc.ThinkingBudget = &budget
		}
		gc.ThinkingConfig = tc
	} else if model.Reasoning && opts.ThinkingLevel == provider.ThinkingOff {
		// getDisabledGoogleThinkingConfig: thinkingBudget:0 unless the model
		// uses discrete levels, in which case Gemini has no "off" level and
		// pi falls back to whatever clampThinkingLevel resolves to; this
		// port always disables via budget 0, which every Gemini model that
		// supports thinkingConfig also accepts.
		zero := 0
		gc.ThinkingConfig = &googleThinkingConfig{ThinkingBudget: &zero}
	}
	if gc.Temperature != nil || gc.MaxOutputTokens > 0 || gc.ThinkingConfig != nil {
		req.GenerationConfig = gc
	}

	return req
}

// convertGoogleMessages ports google-shared.js's convertMessages: user/
// assistant/toolResult -> Gemini Content[], with thought signatures kept
// only when the assistant message that produced them is from the same
// provider+model as the one we're about to call (see resolveThoughtSignature).
func convertGoogleMessages(model provider.Model, transcript []msg.Message) []googleContent {
	var contents []googleContent
	includeID := requiresGoogleToolCallID(model.ID)

	for _, m := range transcript {
		switch t := m.(type) {
		case msg.SystemMessage:
			// folded into systemInstruction

		case msg.UserMessage:
			var parts []googlePart
			for _, b := range t.Content {
				switch c := b.(type) {
				case msg.TextContent:
					parts = append(parts, googlePart{Text: c.Text})
				case msg.ImageContent:
					parts = append(parts, googlePart{InlineData: &googleInlineData{MimeType: c.MimeType, Data: c.Data}})
				}
			}
			if len(parts) == 0 {
				continue
			}
			contents = append(contents, googleContent{Role: "user", Parts: parts})

		case msg.AssistantMessage:
			sameProviderModel := t.Provider == model.Provider && t.Model == model.ID
			var parts []googlePart
			for _, b := range t.Content {
				switch c := b.(type) {
				case msg.TextContent:
					sig := ""
					if sameProviderModel {
						sig = c.TextSignature
					}
					if strings.TrimSpace(c.Text) == "" && sig == "" {
						continue
					}
					parts = append(parts, googlePart{Text: c.Text, ThoughtSignature: sig})
				case msg.ThinkingContent:
					if sameProviderModel {
						sig := c.ThinkingSignature
						if strings.TrimSpace(c.Thinking) == "" && sig == "" {
							continue
						}
						parts = append(parts, googlePart{Thought: true, Text: c.Thinking, ThoughtSignature: sig})
					} else {
						if strings.TrimSpace(c.Thinking) == "" {
							continue
						}
						parts = append(parts, googlePart{Text: c.Thinking})
					}
				case msg.ToolCall:
					sig := ""
					if sameProviderModel {
						sig = c.ThoughtSignature
					}
					fc := &googleFunctionCall{Name: c.Name, Args: c.Arguments}
					if includeID {
						fc.ID = c.ID
					}
					parts = append(parts, googlePart{FunctionCall: fc, ThoughtSignature: sig})
				}
			}
			if len(parts) == 0 {
				continue
			}
			contents = append(contents, googleContent{Role: "model", Parts: parts})

		case msg.ToolResultMessage:
			text := msg.TextOf(t.Content)
			var responseVal string
			if text != "" {
				responseVal = text
			}
			key := "output"
			if t.IsError {
				key = "error"
			}
			fr := &googleFunctionResp{Name: t.ToolName, Response: map[string]any{key: responseVal}}
			if includeID {
				fr.ID = t.ToolCallID
			}
			part := googlePart{FunctionResponse: fr}
			// Cloud Code Assist requires all function responses in a single
			// user turn: merge into the previous content if it's already a
			// user turn holding function responses.
			if n := len(contents); n > 0 && contents[n-1].Role == "user" && hasFunctionResponse(contents[n-1].Parts) {
				contents[n-1].Parts = append(contents[n-1].Parts, part)
			} else {
				contents = append(contents, googleContent{Role: "user", Parts: []googlePart{part}})
			}
		}
	}
	return contents
}

func hasFunctionResponse(parts []googlePart) bool {
	for _, p := range parts {
		if p.FunctionResponse != nil {
			return true
		}
	}
	return false
}

func googleHeaders(model provider.Model, auth Auth) http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("x-goog-api-key", auth.APIKey)
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

type googleStreamChunk struct {
	ResponseID    string                `json:"responseId"`
	Candidates    []googleCandidateWire `json:"candidates"`
	UsageMetadata *googleUsageMetadata  `json:"usageMetadata"`
}

type googleCandidateWire struct {
	Content      *googleContent `json:"content"`
	FinishReason string         `json:"finishReason"`
}

type googleUsageMetadata struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount"`
}

// mapGoogleStopReason mirrors google-shared.js's mapStopReasonString (the
// raw-API-response variant; the SDK-enum variant classifies every non-STOP/
// MAX_TOKENS reason as "error", which is what this ports).
func mapGoogleStopReason(reason string) msg.StopReason {
	switch reason {
	case "STOP":
		return msg.StopStop
	case "MAX_TOKENS":
		return msg.StopLength
	default:
		return msg.StopError
	}
}

// Stream starts a Google Generative AI (Gemini) completion.
func (c *GoogleGenerativeAIClient) Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions, auth Auth) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
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

func (c *GoogleGenerativeAIClient) run(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions, auth Auth, events chan<- msg.StreamEvent) (*msg.AssistantMessage, error) {
	url := strings.TrimRight(model.BaseURL, "/") + "/models/" + model.ID + ":streamGenerateContent?alt=sse"
	return runGoogleGenerateContentStream(ctx, c.httpClient(), provider.ApiGoogleGenerativeAI, url, googleHeaders(model, auth), model, transcript, opts, events)
}

// runGoogleGenerateContentStream is the streamGenerateContent request/
// response loop shared by GoogleGenerativeAIClient and GoogleVertexClient:
// both speak the identical GenerateContentResponse wire shape (see this
// file's doc comment), differing only in endpoint URL, auth header and the
// `api` field recorded on the resulting message.
func runGoogleGenerateContentStream(ctx context.Context, httpClient *http.Client, apiName provider.Api, url string, headers http.Header, model provider.Model, transcript []msg.Message, opts provider.StreamOptions, events chan<- msg.StreamEvent) (*msg.AssistantMessage, error) {
	partial := &msg.AssistantMessage{
		API:        string(apiName),
		Provider:   model.Provider,
		Model:      model.ID,
		Role:       msg.RoleAssistant,
		StopReason: msg.StopPending,
		Content:    msg.Blocks{},
	}

	wireReq := buildGoogleRequest(model, transcript, opts)
	body, err := json.Marshal(wireReq)
	if err != nil {
		return errorOut(partial, events, false, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return errorOut(partial, events, false, err)
	}
	httpReq.Header = headers

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return errorOut(partial, events, ctx.Err() != nil, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		buf, _ := io.ReadAll(resp.Body)
		return errorOut(partial, events, isRetriableStatus(resp.StatusCode), &StatusError{Status: resp.StatusCode, Body: string(buf), Retriable: isRetriableStatus(resp.StatusCode)})
	}

	events <- msg.StreamEvent{Type: msg.EventStart, Partial: partial}

	// currentKind/currentIndex track the block currently accumulating text or
	// thinking deltas, mirroring the JS closure's `currentBlock`.
	currentKind := "" // "" | "text" | "thinking"
	currentIndex := -1
	closeCurrent := func() {
		if currentKind == "" {
			return
		}
		switch currentKind {
		case "text":
			tc := partial.Content[currentIndex].(msg.TextContent)
			events <- msg.StreamEvent{Type: msg.EventTextEnd, ContentIndex: currentIndex, Content: tc.Text, Partial: partial}
		case "thinking":
			tc := partial.Content[currentIndex].(msg.ThinkingContent)
			events <- msg.StreamEvent{Type: msg.EventThinkingEnd, ContentIndex: currentIndex, Content: tc.Thinking, Partial: partial}
		}
		currentKind = ""
		currentIndex = -1
	}

	r := bufio.NewReader(resp.Body)
	streamErr := scanSSE(r, func(ev sseEvent) {
		if ev.Data == "" {
			return
		}
		var chunk googleStreamChunk
		if json.Unmarshal([]byte(ev.Data), &chunk) != nil {
			return
		}
		if partial.ResponseID == "" && chunk.ResponseID != "" {
			partial.ResponseID = chunk.ResponseID
		}
		if len(chunk.Candidates) > 0 {
			cand := chunk.Candidates[0]
			if cand.Content != nil {
				for _, part := range cand.Content.Parts {
					if part.FunctionCall == nil {
						isThinking := part.Thought
						wantKind := "text"
						if isThinking {
							wantKind = "thinking"
						}
						if currentKind != wantKind {
							closeCurrent()
							if isThinking {
								partial.Content = append(partial.Content, msg.Thinking(""))
								currentIndex = len(partial.Content) - 1
								currentKind = "thinking"
								events <- msg.StreamEvent{Type: msg.EventThinkingStart, ContentIndex: currentIndex, Partial: partial}
							} else {
								partial.Content = append(partial.Content, msg.Text(""))
								currentIndex = len(partial.Content) - 1
								currentKind = "text"
								events <- msg.StreamEvent{Type: msg.EventTextStart, ContentIndex: currentIndex, Partial: partial}
							}
						}
						if currentKind == "thinking" {
							tc := partial.Content[currentIndex].(msg.ThinkingContent)
							tc.Thinking += part.Text
							if part.ThoughtSignature != "" {
								tc.ThinkingSignature = part.ThoughtSignature
							}
							partial.Content[currentIndex] = tc
							events <- msg.StreamEvent{Type: msg.EventThinkingDelta, ContentIndex: currentIndex, Delta: part.Text, Partial: partial}
						} else {
							tc := partial.Content[currentIndex].(msg.TextContent)
							tc.Text += part.Text
							if part.ThoughtSignature != "" {
								tc.TextSignature = part.ThoughtSignature
							}
							partial.Content[currentIndex] = tc
							events <- msg.StreamEvent{Type: msg.EventTextDelta, ContentIndex: currentIndex, Delta: part.Text, Partial: partial}
						}
						continue
					}

					// functionCall part: close whatever text/thinking block
					// was open, then emit the tool call as one atomic
					// start/delta/end burst -- Gemini does not stream partial
					// function-call JSON the way Anthropic/OpenAI do.
					closeCurrent()
					id := part.FunctionCall.ID
					if id == "" || toolCallIDUsed(partial.Content, id) {
						id = fmt.Sprintf("%s_%d", part.FunctionCall.Name, len(partial.Content))
					}
					tc := msg.NewToolCall(id, part.FunctionCall.Name, part.FunctionCall.Args)
					tc.ThoughtSignature = part.ThoughtSignature
					partial.Content = append(partial.Content, tc)
					pos := len(partial.Content) - 1
					events <- msg.StreamEvent{Type: msg.EventToolCallStart, ContentIndex: pos, Partial: partial}
					argsJSON, _ := json.Marshal(tc.Arguments)
					events <- msg.StreamEvent{Type: msg.EventToolCallDelta, ContentIndex: pos, Delta: string(argsJSON), Partial: partial}
					events <- msg.StreamEvent{Type: msg.EventToolCallEnd, ContentIndex: pos, ToolCall: &tc, Partial: partial}
				}
			}
			if cand.FinishReason != "" {
				partial.RawStopReason = cand.FinishReason
				reason := mapGoogleStopReason(cand.FinishReason)
				if reason == msg.StopStop && hasToolCall(partial.Content) {
					reason = msg.StopToolUse
				}
				partial.StopReason = reason
			}
		}
		if chunk.UsageMetadata != nil {
			u := chunk.UsageMetadata
			partial.Usage.Input = u.PromptTokenCount - u.CachedContentTokenCount
			partial.Usage.Output = u.CandidatesTokenCount + u.ThoughtsTokenCount
			partial.Usage.CacheRead = u.CachedContentTokenCount
			partial.Usage.CacheWrite = 0
			reasoning := u.ThoughtsTokenCount
			partial.Usage.Reasoning = &reasoning
			partial.Usage.TotalTokens = u.TotalTokenCount
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
	closeCurrent()

	if ctx.Err() != nil {
		return errorOut(partial, events, true, fmt.Errorf("request was aborted"))
	}
	if partial.StopReason == msg.StopPending {
		return errorOut(partial, events, false, fmt.Errorf("google stream ended without a finish reason"))
	}
	if partial.StopReason == msg.StopAborted || partial.StopReason == msg.StopError {
		errMsg := partial.ErrorMessage
		if errMsg == "" {
			if partial.RawStopReason != "" {
				errMsg = fmt.Sprintf("Provider stopped with: %s", partial.RawStopReason)
			} else {
				errMsg = "an unknown error occurred"
			}
			partial.ErrorMessage = errMsg
		}
		events <- msg.StreamEvent{Type: msg.EventError, Reason: partial.StopReason, Error: partial}
		return nil, errors.New(errMsg)
	}

	events <- msg.StreamEvent{Type: msg.EventDone, Reason: partial.StopReason, Message: partial}
	return partial, nil
}

func toolCallIDUsed(blocks msg.Blocks, id string) bool {
	for _, b := range blocks {
		if tc, ok := b.(msg.ToolCall); ok && tc.ID == id {
			return true
		}
	}
	return false
}

func hasToolCall(blocks msg.Blocks) bool {
	for _, b := range blocks {
		if _, ok := b.(msg.ToolCall); ok {
			return true
		}
	}
	return false
}
