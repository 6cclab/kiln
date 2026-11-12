package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/session"
)

func TestResponsesTextIdentityStreamStoreReplay(t *testing.T) {
	// The phase on output_item.done is authoritative; output_item.added also
	// supplies identity for an interrupted stream.
	sse := []byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg_original\",\"phase\":\"commentary\"}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"hello\"}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg_original\",\"phase\":\"final_answer\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\"}}\n\n")

	cases := []struct {
		name   string
		api    provider.Api
		client interface {
			Stream(context.Context, provider.Model, []msg.Message, provider.StreamOptions, Auth) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error))
		}
		model func(string) provider.Model
		auth  func(*testing.T) Auth
		input func(provider.Model, msg.AssistantMessage) []json.RawMessage
	}{
		{"openai", provider.ApiOpenAIResponses, &OpenAIResponsesClient{}, responsesModel, func(*testing.T) Auth { return Auth{APIKey: "k"} }, func(m provider.Model, a msg.AssistantMessage) []json.RawMessage {
			return buildOpenAIResponsesRequest(m, []msg.Message{a}, provider.StreamOptions{}).Input
		}},
		{"codex", provider.ApiOpenAICodexResponses, &OpenAICodexResponsesClient{}, codexCostedModel, func(t *testing.T) Auth { return testAuth(t, "acct_1") }, func(m provider.Model, a msg.AssistantMessage) []json.RawMessage {
			items := buildCodexRequest(m, []msg.Message{a}, provider.StreamOptions{}).Input
			out := make([]json.RawMessage, 0, len(items))
			for _, item := range items {
				b, _ := json.Marshal(item)
				out = append(out, b)
			}
			return out
		}},
		{"azure", provider.ApiAzureOpenAIResponses, &AzureOpenAIResponsesClient{}, func(url string) provider.Model { return azureCostedModel(url, false) }, func(*testing.T) Auth { return Auth{APIKey: "k"} }, func(m provider.Model, a msg.AssistantMessage) []json.RawMessage {
			return buildAzureRequest(m, []msg.Message{a}, provider.StreamOptions{}).Input
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := replaySSE(t, http.StatusOK, "text/event-stream", sse)
			model := tc.model(srv.URL)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			events, wait := tc.client.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{}, tc.auth(t))
			sawDelta := false
			for ev := range events {
				if ev.Type == msg.EventTextDelta {
					sawDelta = true
				}
			}
			final, err := wait()
			if err != nil {
				t.Fatal(err)
			}
			if !sawDelta || final.API != string(tc.api) {
				t.Fatalf("sawDelta=%v API=%q", sawDelta, final.API)
			}
			// Session entries are the actual JSONL wire shape; the signature
			// survives the entry envelope as well as the message codec.
			var line bytes.Buffer
			entry := session.Entry{ID: "entry_1", Type: session.EntryMessage, Message: *final}
			if err := json.NewEncoder(&line).Encode(entry); err != nil {
				t.Fatal(err)
			}
			var storedEntry session.Entry
			if err := json.NewDecoder(bufio.NewReader(&line)).Decode(&storedEntry); err != nil {
				t.Fatal(err)
			}
			stored, ok := storedEntry.Message.(msg.AssistantMessage)
			if !ok {
				t.Fatalf("stored message type = %T", storedEntry.Message)
			}
			items := tc.input(model, stored)
			if len(items) != 1 {
				t.Fatalf("input items = %d, want 1", len(items))
			}
			var item map[string]any
			if err := json.Unmarshal(items[0], &item); err != nil {
				t.Fatal(err)
			}
			if item["id"] != "msg_original" || item["phase"] != "final_answer" {
				t.Fatalf("replayed identity = %+v", item)
			}

			// Legacy and mismatched origins must never carry provider metadata.
			for name, mutate := range map[string]func(*msg.AssistantMessage){
				"legacy": func(a *msg.AssistantMessage) {
					c := a.Content[0].(msg.TextContent)
					c.TextSignature = ""
					a.Content[0] = c
				},
				"other API":      func(a *msg.AssistantMessage) { a.API = "google-generative-ai" },
				"other provider": func(a *msg.AssistantMessage) { a.Provider = "another-provider" },
				"other model":    func(a *msg.AssistantMessage) { a.Model = "another-model" },
				"edited text": func(a *msg.AssistantMessage) {
					c := a.Content[0].(msg.TextContent)
					c.Text = "changed"
					a.Content[0] = c
				},
				"foreign signature": func(a *msg.AssistantMessage) {
					c := a.Content[0].(msg.TextContent)
					c.TextSignature = "google-thought-token"
					a.Content[0] = c
				},
			} {
				t.Run(name, func(t *testing.T) {
					a := stored
					a.Content = append(msg.Blocks(nil), stored.Content...)
					mutate(&a)
					var fallback map[string]any
					if err := json.Unmarshal(tc.input(model, a)[0], &fallback); err != nil {
						t.Fatal(err)
					}
					if fallback["id"] != "msg_pi_0" {
						t.Fatalf("fallback ID = %v", fallback["id"])
					}
					if _, ok := fallback["phase"]; ok {
						t.Fatalf("invented/leaked phase: %+v", fallback)
					}
				})
			}
			t.Run("interrupted", func(t *testing.T) {
				truncated := bytes.Split(sse, []byte("event: response.output_item.done"))[0]
				interruptServer := replaySSE(t, http.StatusOK, "text/event-stream", truncated)
				interruptModel := tc.model(interruptServer.URL)
				evch, wait := tc.client.Stream(ctx, interruptModel, nil, provider.StreamOptions{}, tc.auth(t))
				var partial *msg.AssistantMessage
				for ev := range evch {
					if ev.Type == msg.EventError {
						partial = ev.Error
					}
				}
				if _, err := wait(); err == nil || partial == nil {
					t.Fatal("expected interrupted partial")
				}
				c := partial.Content[0].(msg.TextContent)
				if sig, ok := parseResponsesTextSignature(c, *partial, interruptModel, tc.api); !ok || sig.ID != "msg_original" || sig.Phase != "commentary" {
					t.Fatalf("interrupted signature = %+v", c)
				}
			})
			t.Run("unknown done phase", func(t *testing.T) {
				unknown := []byte(strings.Replace(string(sse), `"phase":"final_answer"`, `"phase":"unknown_phase"`, 1))
				unknownServer := replaySSE(t, http.StatusOK, "text/event-stream", unknown)
				unknownModel := tc.model(unknownServer.URL)
				evch, wait := tc.client.Stream(ctx, unknownModel, nil, provider.StreamOptions{}, tc.auth(t))
				for range evch {
				}
				answer, err := wait()
				if err != nil {
					t.Fatal(err)
				}
				var item map[string]any
				if err := json.Unmarshal(tc.input(unknownModel, *answer)[0], &item); err != nil {
					t.Fatal(err)
				}
				if item["id"] != "msg_pi_0" {
					t.Fatalf("unknown phase retained ID: %+v", item)
				}
				if _, ok := item["phase"]; ok {
					t.Fatalf("unknown phase leaked: %+v", item)
				}
			})
			t.Run("done omits phase", func(t *testing.T) {
				withoutDonePhase := []byte(strings.Replace(string(sse), `"phase":"final_answer",`, "", 1))
				server := replaySSE(t, http.StatusOK, "text/event-stream", withoutDonePhase)
				m := tc.model(server.URL)
				evch, wait := tc.client.Stream(ctx, m, nil, provider.StreamOptions{}, tc.auth(t))
				for range evch {
				}
				answer, err := wait()
				if err != nil {
					t.Fatal(err)
				}
				var item map[string]any
				if err := json.Unmarshal(tc.input(m, *answer)[0], &item); err != nil {
					t.Fatal(err)
				}
				if item["id"] != "msg_original" || item["phase"] != "commentary" {
					t.Fatalf("added phase lost: %+v", item)
				}
			})
		})
	}
}

func TestResponsesTextSignatureNeverBecomesGoogleThoughtSignature(t *testing.T) {
	model := googleModel("https://example.test")
	responsesSource := msg.AssistantMessage{
		API: string(provider.ApiOpenAIResponses), Provider: model.Provider,
		Model: model.ID, Role: msg.RoleAssistant,
	}
	foreign := stampResponsesText(msg.Text("hello"), responsesSource, "msg_original", "commentary")
	if foreign.TextSignature == "" {
		t.Fatal("expected Responses signature")
	}

	cases := []struct {
		name      string
		api       provider.Api
		sourceAPI provider.Api
		text      msg.TextContent
		want      string
	}{
		{"responses to google", provider.ApiGoogleGenerativeAI, provider.ApiOpenAIResponses, foreign, ""},
		{"responses to vertex", provider.ApiGoogleVertex, provider.ApiOpenAIResponses, foreign, ""},
		{"genuine google legacy", provider.ApiGoogleGenerativeAI, provider.ApiGoogleGenerativeAI, msg.TextContent{Type: "text", Text: "hello", TextSignature: "opaque-google-signature"}, "opaque-google-signature"},
		{"genuine vertex legacy", provider.ApiGoogleVertex, provider.ApiGoogleVertex, msg.TextContent{Type: "text", Text: "hello", TextSignature: "opaque-vertex-signature"}, "opaque-vertex-signature"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := model
			m.Api = tc.api
			a := msg.AssistantMessage{API: string(tc.sourceAPI), Provider: m.Provider, Model: m.ID, Role: msg.RoleAssistant, Content: msg.Blocks{tc.text}}
			contents := convertGoogleMessages(m, []msg.Message{a})
			if len(contents) != 1 || len(contents[0].Parts) != 1 {
				t.Fatalf("contents = %+v", contents)
			}
			if got := contents[0].Parts[0].ThoughtSignature; got != tc.want {
				t.Fatalf("thoughtSignature = %q, want %q", got, tc.want)
			}
		})
	}
}
