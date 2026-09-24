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

// pi-messages API client, ported from pi-ai's dist/api/pi-messages.js: pi's
// own wire protocol, spoken by the Radius gateway (or any backend
// implementing it). Quoting the file's own doc comment (pi-messages.js:1-10):
//
//	"Streams pi's own message protocol directly to a backend: the request is
//	a single POST of `{ model, context, options }` to `<baseUrl>/messages`,
//	the response is an SSE stream of serialized assistant-message events plus
//	a terminal `done`/`error` event."
//
// Because pi's own AssistantMessageEvent/Message JSON shapes are exactly
// what internal/msg's types already serialize to (see msg package's doc
// comment: "Field names in JSON are pi's, not Go's"), the request's
// `context.messages` is the transcript marshaled as-is, and the response
// events decode almost directly into msg.StreamEvent -- there is no
// provider-specific request/response shape to reverse-engineer here, unlike
// the other three shapes in this phase.
type PiMessagesClient struct {
	HTTPClient *http.Client
}

func (c *PiMessagesClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// --- wire request shape ---

type piMessagesContext struct {
	Messages []msg.Message `json:"messages"`
}

type piMessagesOptions struct {
	Temperature    *float64               `json:"temperature,omitempty"`
	MaxTokens      int                    `json:"maxTokens,omitempty"`
	Reasoning      provider.ThinkingLevel `json:"reasoning,omitempty"`
	CacheRetention string                 `json:"cacheRetention,omitempty"`
}

type piMessagesRequest struct {
	Model   string            `json:"model"`
	Context piMessagesContext `json:"context"`
	Options piMessagesOptions `json:"options"`
}

// --- wire response shape ---
//
// One SSE `data:` line per pi AssistantMessageEvent
// (pi-messages.js:parsePiMessagesEvent / createEventConverter's input
// shape), keyed by `type` exactly like msg.EventType.

type piEventWire struct {
	Type                  string          `json:"type"`
	ContentIndex          int             `json:"contentIndex"`
	Delta                 string          `json:"delta"`
	Content               string          `json:"content"`
	ContentSignature      string          `json:"contentSignature"`
	Redacted              bool            `json:"redacted"`
	ID                    string          `json:"id"`
	ToolName              string          `json:"toolName"`
	ToolCall              json.RawMessage `json:"toolCall"`
	Reason                string          `json:"reason"`
	Usage                 *msg.Usage      `json:"usage"`
	ResponseID            string          `json:"responseId"`
	ProviderThinkingLevel string          `json:"providerThinkingLevel"`
	ErrorMessage          string          `json:"errorMessage"`
}

func piMessagesHeaders(model provider.Model, auth Auth) http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "text/event-stream")
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

// Stream starts a pi-messages completion.
func (c *PiMessagesClient) Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions, auth Auth) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
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

func (c *PiMessagesClient) run(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions, auth Auth, events chan<- msg.StreamEvent) (*msg.AssistantMessage, error) {
	partial := &msg.AssistantMessage{
		API:        string(provider.ApiPiMessages),
		Provider:   model.Provider,
		Model:      model.ID,
		Role:       msg.RoleAssistant,
		StopReason: msg.StopPending,
		Content:    msg.Blocks{},
	}

	wireReq := piMessagesRequest{
		Model:   model.ID,
		Context: piMessagesContext{Messages: transcript},
		Options: piMessagesOptions{
			Temperature: opts.Temperature,
			MaxTokens:   opts.MaxTokens,
			Reasoning:   opts.ThinkingLevel,
		},
	}
	body, err := json.Marshal(wireReq)
	if err != nil {
		return errorOut(partial, events, false, err)
	}

	url := strings.TrimRight(model.BaseURL, "/") + "/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return errorOut(partial, events, false, err)
	}
	httpReq.Header = piMessagesHeaders(model, auth)

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

	toolJSON := map[int]string{}
	sawTerminal := false

	r := bufio.NewReader(resp.Body)
	streamErr := scanSSE(r, func(ev sseEvent) {
		data := strings.TrimSpace(ev.Data)
		if data == "" || data == "[DONE]" {
			return
		}
		var w piEventWire
		if json.Unmarshal([]byte(data), &w) != nil {
			return
		}

		switch msg.EventType(w.Type) {
		case msg.EventStart:
			// already emitted our own start above.

		case msg.EventTextStart:
			partial.Content = growContent(partial.Content, w.ContentIndex, msg.Text(""))
			events <- msg.StreamEvent{Type: msg.EventTextStart, ContentIndex: w.ContentIndex, Partial: partial}

		case msg.EventTextDelta:
			tc := partial.Content[w.ContentIndex].(msg.TextContent)
			tc.Text += w.Delta
			partial.Content[w.ContentIndex] = tc
			events <- msg.StreamEvent{Type: msg.EventTextDelta, ContentIndex: w.ContentIndex, Delta: w.Delta, Partial: partial}

		case msg.EventTextEnd:
			tc := partial.Content[w.ContentIndex].(msg.TextContent)
			tc.Text = w.Content
			tc.TextSignature = w.ContentSignature
			partial.Content[w.ContentIndex] = tc
			events <- msg.StreamEvent{Type: msg.EventTextEnd, ContentIndex: w.ContentIndex, Content: w.Content, Partial: partial}

		case msg.EventThinkingStart:
			partial.Content = growContent(partial.Content, w.ContentIndex, msg.Thinking(""))
			events <- msg.StreamEvent{Type: msg.EventThinkingStart, ContentIndex: w.ContentIndex, Partial: partial}

		case msg.EventThinkingDelta:
			tc := partial.Content[w.ContentIndex].(msg.ThinkingContent)
			tc.Thinking += w.Delta
			partial.Content[w.ContentIndex] = tc
			events <- msg.StreamEvent{Type: msg.EventThinkingDelta, ContentIndex: w.ContentIndex, Delta: w.Delta, Partial: partial}

		case msg.EventThinkingEnd:
			tc := partial.Content[w.ContentIndex].(msg.ThinkingContent)
			tc.Thinking = w.Content
			tc.ThinkingSignature = w.ContentSignature
			tc.Redacted = w.Redacted
			partial.Content[w.ContentIndex] = tc
			events <- msg.StreamEvent{Type: msg.EventThinkingEnd, ContentIndex: w.ContentIndex, Content: w.Content, Partial: partial}

		case msg.EventToolCallStart:
			partial.Content = growContent(partial.Content, w.ContentIndex, msg.NewToolCall(w.ID, w.ToolName, nil))
			toolJSON[w.ContentIndex] = ""
			events <- msg.StreamEvent{Type: msg.EventToolCallStart, ContentIndex: w.ContentIndex, Partial: partial}

		case msg.EventToolCallDelta:
			toolJSON[w.ContentIndex] += w.Delta
			tc := partial.Content[w.ContentIndex].(msg.ToolCall)
			var args map[string]any
			if json.Unmarshal([]byte(toolJSON[w.ContentIndex]), &args) == nil {
				tc.Arguments = args
			}
			partial.Content[w.ContentIndex] = tc
			events <- msg.StreamEvent{Type: msg.EventToolCallDelta, ContentIndex: w.ContentIndex, Delta: w.Delta, Partial: partial}

		case msg.EventToolCallEnd:
			var final msg.ToolCall
			if len(w.ToolCall) > 0 {
				_ = json.Unmarshal(w.ToolCall, &final)
			}
			if existing, ok := partial.Content[w.ContentIndex].(msg.ToolCall); ok {
				if final.ID == "" {
					final.ID = existing.ID
				}
				if final.Name == "" {
					final.Name = existing.Name
				}
				if final.Arguments == nil {
					final.Arguments = existing.Arguments
				}
			}
			final.Type = "toolCall"
			partial.Content[w.ContentIndex] = final
			delete(toolJSON, w.ContentIndex)
			events <- msg.StreamEvent{Type: msg.EventToolCallEnd, ContentIndex: w.ContentIndex, ToolCall: &final, Partial: partial}

		case msg.EventDone:
			sawTerminal = true
			partial.StopReason = msg.StopReason(w.Reason)
			if w.Usage != nil {
				partial.Usage = *w.Usage
			}
			partial.ResponseID = w.ResponseID
			partial.ProviderThinkingLevel = w.ProviderThinkingLevel

		case msg.EventError:
			sawTerminal = true
			partial.StopReason = msg.StopReason(w.Reason)
			if w.Usage != nil {
				partial.Usage = *w.Usage
			}
			partial.ResponseID = w.ResponseID
			partial.ErrorMessage = w.ErrorMessage
		}
	})
	if streamErr != io.EOF {
		return errorOut(partial, events, ctx.Err() != nil, streamErr)
	}

	if ctx.Err() != nil {
		return errorOut(partial, events, true, fmt.Errorf("request was aborted"))
	}
	if !sawTerminal {
		return errorOut(partial, events, false, fmt.Errorf("pi-messages stream ended without a terminal event"))
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

// growContent grows blocks to index+1 (pi's events are always in content
// order so this never has to insert before the end) and sets blocks[index].
func growContent(blocks msg.Blocks, index int, c msg.Content) msg.Blocks {
	for len(blocks) <= index {
		blocks = append(blocks, nil)
	}
	blocks[index] = c
	return blocks
}
