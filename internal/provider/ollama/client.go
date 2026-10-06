package ollama

// Native /api/chat streaming client.
//
// Ollama's native chat protocol, not the OpenAI-compatible /v1 shape,
// because /v1 cannot carry options.num_ctx (see ollama.go's package
// comment and the finding this fix closes:
// qa/findings/20261006T141123Z-ollama-context-window-mismatch.json).
// Verified against a live Ollama 0.34.0 (qwen3:8b): the request/response
// shapes below are the actual wire format, not a port of pi-ai (Ollama is
// not in pi-ai's API-shape catalog).

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
	"github.com/andrepato/harness/internal/provider/api"
)

// nativeClient streams completions against one Ollama host's native
// /api/chat endpoint.
type nativeClient struct {
	HTTPClient *http.Client
}

func (c *nativeClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// --- wire request shapes ---

type nativeToolCallFunction struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
	// Index disambiguates parallel tool calls in a single response. Ollama
	// assigns it; this client only ever reads it back.
	Index int `json:"index,omitempty"`
}

type nativeToolCall struct {
	// ID is Ollama's own id for the call (e.g. "call_l50vzj24"), present on
	// the way out of the model. Replayed verbatim when this call's
	// assistant message re-enters a later request's transcript, and kiln's
	// own msg.ToolCall.ID always has a value by the time it is replayed
	// (internal/harness assigns one), so this is never sent empty in
	// practice; omitempty just keeps a test fixture that leaves it unset
	// from sending a bare "".
	ID       string                 `json:"id,omitempty"`
	Type     string                 `json:"type,omitempty"`
	Function nativeToolCallFunction `json:"function"`
}

type nativeMessage struct {
	Role string `json:"role"`
	// Content is always a plain string: Ollama's native protocol has no
	// multipart content array (that is /v1's shape). An image rides
	// alongside in Images, not inline in Content.
	Content   string           `json:"content,omitempty"`
	Thinking  string           `json:"thinking,omitempty"`
	Images    []string         `json:"images,omitempty"`
	ToolCalls []nativeToolCall `json:"tool_calls,omitempty"`
	// ToolName names which tool a role:"tool" message answers. Ollama's docs
	// call this field "tool_name"; sending it is harmless even when a given
	// server build ignores it.
	ToolName string `json:"tool_name,omitempty"`
}

type nativeToolFunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type nativeTool struct {
	Type     string                `json:"type"`
	Function nativeToolFunctionDef `json:"function"`
}

type nativeOptions struct {
	// NumCtx is the fix this client exists for: the context window kiln
	// asks Ollama to serve, sent on every request so the served window is
	// always the one kiln budgeted for (internal/budget.TierForWindow),
	// never a stale or server-default one.
	NumCtx int `json:"num_ctx,omitempty"`
	// NumPredict is Ollama's max-output-tokens knob (OpenAI's max_tokens
	// equivalent).
	NumPredict  int      `json:"num_predict,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
}

type nativeChatRequest struct {
	Model    string          `json:"model"`
	Messages []nativeMessage `json:"messages"`
	Tools    []nativeTool    `json:"tools,omitempty"`
	Stream   bool            `json:"stream"`
	// Think is Ollama's native reasoning switch: a bare bool, unlike every
	// thinkingFormat variant openai_completions.go has to juggle. Sent
	// explicitly (never omitted) for a reasoning-capable model so the
	// request never leaves the server to default it one way or the other;
	// left nil for a model with no thinking capability, matching
	// openai_completions.go's own "model.Reasoning gates whether anything
	// thinking-shaped is sent at all" rule.
	Think   *bool          `json:"think,omitempty"`
	Options *nativeOptions `json:"options,omitempty"`
}

func buildNativeRequest(model provider.Model, transcript []msg.Message, opts provider.StreamOptions) nativeChatRequest {
	req := nativeChatRequest{Model: model.ID, Stream: true}

	numCtx := model.ContextWindow
	maxTokens := model.MaxTokens
	if opts.MaxTokens > 0 {
		maxTokens = opts.MaxTokens
	}
	if numCtx > 0 || maxTokens > 0 || opts.Temperature != nil {
		req.Options = &nativeOptions{NumCtx: numCtx, NumPredict: maxTokens, Temperature: opts.Temperature}
	}

	if model.Reasoning {
		think := opts.ThinkingLevel != "" && opts.ThinkingLevel != provider.ThinkingOff
		req.Think = &think
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
		req.Messages = append(req.Messages, nativeMessage{Role: "system", Content: systemText})
	}

	for _, m := range transcript {
		switch t := m.(type) {
		case msg.SystemMessage:
			// folded above
		case msg.UserMessage:
			req.Messages = append(req.Messages, userToNativeMessage(t))
		case msg.AssistantMessage:
			req.Messages = append(req.Messages, assistantToNativeMessage(t))
		case msg.ToolResultMessage:
			req.Messages = append(req.Messages, nativeMessage{
				Role:     "tool",
				Content:  msg.TextOf(t.Content),
				ToolName: t.ToolName,
			})
		}
	}

	for _, td := range opts.Tools {
		if len(td.ServerTool) > 0 {
			// No provider-native-tool equivalent on Ollama; skip, matching
			// every non-Anthropic client.
			continue
		}
		schema := td.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		req.Tools = append(req.Tools, nativeTool{
			Type: "function",
			Function: nativeToolFunctionDef{
				Name:        td.Name,
				Description: td.Description,
				Parameters:  schema,
			},
		})
	}

	return req
}

func userToNativeMessage(t msg.UserMessage) nativeMessage {
	nm := nativeMessage{Role: "user"}
	var text string
	var images []string
	for _, b := range t.Content {
		switch c := b.(type) {
		case msg.TextContent:
			if text != "" {
				text += "\n"
			}
			text += c.Text
		case msg.ImageContent:
			// Native images are bare base64, no data: URI wrapper -- that
			// wrapping is /v1's image_url shape.
			images = append(images, c.Data)
		}
	}
	nm.Content = text
	nm.Images = images
	return nm
}

func assistantToNativeMessage(t msg.AssistantMessage) nativeMessage {
	nm := nativeMessage{Role: "assistant"}
	var text string
	var calls []nativeToolCall
	for _, b := range t.Content {
		switch c := b.(type) {
		case msg.TextContent:
			text += c.Text
		case msg.ToolCall:
			calls = append(calls, nativeToolCall{
				ID:       c.ID,
				Type:     "function",
				Function: nativeToolCallFunction{Name: c.Name, Arguments: c.Arguments},
			})
		}
	}
	nm.Content = text
	nm.ToolCalls = calls
	return nm
}

func nativeHeaders(model provider.Model, auth api.Auth) http.Header {
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

// nativeChatChunk is one NDJSON line from a streaming /api/chat response.
// On a non-error line, Message carries this delta's content (the whole
// tool-call array when one is present: Ollama does not stream partial
// tool-call-argument fragments the way /v1 does, so each tool call arrives
// fully formed in one chunk). Error carries a mid-stream failure (e.g. the
// model crashed, or ran out of memory) delivered as a trailing NDJSON line
// rather than an HTTP-level failure.
type nativeChatChunk struct {
	Message struct {
		Role      string           `json:"role"`
		Content   string           `json:"content"`
		Thinking  string           `json:"thinking"`
		ToolCalls []nativeToolCall `json:"tool_calls"`
	} `json:"message"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
	Error           string `json:"error"`
}

func mapNativeDoneReason(reason string, hasToolCalls bool) (msg.StopReason, string) {
	if hasToolCalls {
		return msg.StopToolUse, ""
	}
	switch reason {
	case "", "stop":
		return msg.StopStop, ""
	case "length":
		return msg.StopLength, ""
	default:
		return msg.StopError, fmt.Sprintf("Provider done_reason: %s", reason)
	}
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

func isRetriableStatus(status int) bool {
	return status == 429 || status == 529 || status >= 500
}

// Stream starts a native Ollama chat completion.
func (c *nativeClient) Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions, auth api.Auth) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
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

func (c *nativeClient) run(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions, auth api.Auth, events chan<- msg.StreamEvent) (*msg.AssistantMessage, error) {
	partial := &msg.AssistantMessage{
		API:        string(ApiOllamaNative),
		Provider:   model.Provider,
		Model:      model.ID,
		Role:       msg.RoleAssistant,
		StopReason: msg.StopPending,
		Content:    msg.Blocks{},
	}

	wireReq := buildNativeRequest(model, transcript, opts)
	body, err := json.Marshal(wireReq)
	if err != nil {
		return errorOut(partial, events, false, err)
	}

	url := strings.TrimRight(model.BaseURL, "/") + "/api/chat"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return errorOut(partial, events, false, err)
	}
	httpReq.Header = nativeHeaders(model, auth)

	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return errorOut(partial, events, ctx.Err() != nil, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		buf, _ := io.ReadAll(resp.Body)
		return errorOut(partial, events, isRetriableStatus(resp.StatusCode), &api.StatusError{Status: resp.StatusCode, Body: string(buf), Retriable: isRetriableStatus(resp.StatusCode)})
	}

	events <- msg.StreamEvent{Type: msg.EventStart, Partial: partial}

	var textIndex = -1
	var thinkingIndex = -1
	toolPosOf := map[int]int{} // ollama tool-call index -> partial.Content position
	sawFinish := false
	var midStreamErr string

	reader := bufio.NewReaderSize(resp.Body, 64*1024)
	var readErr error
	for {
		line, err := reader.ReadString('\n')
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			var chunk nativeChatChunk
			if jsonErr := json.Unmarshal([]byte(trimmed), &chunk); jsonErr == nil {
				if chunk.Error != "" {
					midStreamErr = chunk.Error
					readErr = io.EOF // stop reading; treated as a clean end below
					break
				}

				if chunk.Message.Content != "" {
					if textIndex == -1 {
						partial.Content = append(partial.Content, msg.Text(""))
						textIndex = len(partial.Content) - 1
						events <- msg.StreamEvent{Type: msg.EventTextStart, ContentIndex: textIndex, Partial: partial}
					}
					tc := partial.Content[textIndex].(msg.TextContent)
					tc.Text += chunk.Message.Content
					partial.Content[textIndex] = tc
					events <- msg.StreamEvent{Type: msg.EventTextDelta, ContentIndex: textIndex, Delta: chunk.Message.Content, Partial: partial}
				}

				if chunk.Message.Thinking != "" {
					if thinkingIndex == -1 {
						partial.Content = append(partial.Content, msg.Thinking(""))
						thinkingIndex = len(partial.Content) - 1
						events <- msg.StreamEvent{Type: msg.EventThinkingStart, ContentIndex: thinkingIndex, Partial: partial}
					}
					tc := partial.Content[thinkingIndex].(msg.ThinkingContent)
					tc.Thinking += chunk.Message.Thinking
					partial.Content[thinkingIndex] = tc
					events <- msg.StreamEvent{Type: msg.EventThinkingDelta, ContentIndex: thinkingIndex, Delta: chunk.Message.Thinking, Partial: partial}
				}

				for i, tcWire := range chunk.Message.ToolCalls {
					key := tcWire.Function.Index
					if key == 0 && len(chunk.Message.ToolCalls) > 1 {
						// Some responses omit Index on a single call but
						// always set it correctly when there is more than
						// one; a lone unset Index is just "the first call".
						key = i
					}
					pos, ok := toolPosOf[key]
					if !ok {
						partial.Content = append(partial.Content, msg.NewToolCall(tcWire.ID, tcWire.Function.Name, tcWire.Function.Arguments))
						pos = len(partial.Content) - 1
						toolPosOf[key] = pos
						events <- msg.StreamEvent{Type: msg.EventToolCallStart, ContentIndex: pos, Partial: partial}
					}
					call := partial.Content[pos].(msg.ToolCall)
					if tcWire.ID != "" {
						call.ID = tcWire.ID
					}
					if tcWire.Function.Name != "" {
						call.Name = tcWire.Function.Name
					}
					if tcWire.Function.Arguments != nil {
						call.Arguments = tcWire.Function.Arguments
					}
					partial.Content[pos] = call
					argsJSON, _ := json.Marshal(tcWire.Function.Arguments)
					events <- msg.StreamEvent{Type: msg.EventToolCallDelta, ContentIndex: pos, Delta: string(argsJSON), Partial: partial}
				}

				if chunk.Done {
					sawFinish = true
					partial.RawStopReason = chunk.DoneReason
					if chunk.PromptEvalCount > 0 || chunk.EvalCount > 0 {
						partial.Usage.Input = chunk.PromptEvalCount
						partial.Usage.Output = chunk.EvalCount
						partial.Usage.TotalTokens = chunk.PromptEvalCount + chunk.EvalCount
						provider.ApplyCost(model, &partial.Usage)
					}
					reason, errMsg := mapNativeDoneReason(chunk.DoneReason, len(toolPosOf) > 0)
					partial.StopReason = reason
					if errMsg != "" {
						partial.ErrorMessage = errMsg
					}
				}
			}
		}
		if err != nil {
			readErr = err
			break
		}
	}

	if readErr != io.EOF {
		if ctx.Err() != nil {
			return errorOut(partial, events, true, readErr)
		}
		return errorOut(partial, events, false, provider.StreamInterrupted{Cause: readErr})
	}

	for pos := range toolPosOf {
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

	if midStreamErr != "" {
		return errorOut(partial, events, ctx.Err() != nil, errors.New(midStreamErr))
	}
	if ctx.Err() != nil {
		return errorOut(partial, events, true, fmt.Errorf("request was aborted"))
	}
	if !sawFinish {
		return errorOut(partial, events, false, fmt.Errorf("stream ended without a done message"))
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
