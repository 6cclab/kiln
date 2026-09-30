package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// ANTHROPIC_BASE_URL routes Anthropic's own models to the given endpoint,
// as Claude Code does; other Messages-API providers keep their own.
func TestAnthropicBaseURL_EnvOverridesAnthropicOnly(t *testing.T) {
	anthropic := provider.Model{Provider: "anthropic", BaseURL: "https://api.anthropic.com"}
	other := provider.Model{Provider: "minimax", BaseURL: "https://api.minimax.io/anthropic"}

	t.Setenv("ANTHROPIC_BASE_URL", "")
	if got := anthropicBaseURL(anthropic); got != anthropic.BaseURL {
		t.Errorf("unset: got %q, want the catalog URL", got)
	}
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:9999")
	if got := anthropicBaseURL(anthropic); got != "http://127.0.0.1:9999" {
		t.Errorf("set: got %q, want the env URL", got)
	}
	if got := anthropicBaseURL(other); got != other.BaseURL {
		t.Errorf("non-anthropic provider: got %q, want its own URL", got)
	}
}

// The request itself goes to ANTHROPIC_BASE_URL + /v1/messages.
func TestAnthropicStream_SendsToEnvBaseURL(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/messages" {
			hits.Add(1)
		}
		http.Error(w, `{"type":"error","error":{"type":"api_error","message":"test"}}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)

	model := provider.Model{ID: "claude-opus-4-8", Provider: "anthropic", Api: provider.ApiAnthropicMessages,
		BaseURL: "https://api.anthropic.invalid", MaxTokens: 1024}
	transcript := []msg.Message{msg.UserMessage{Content: msg.Blocks{msg.Text("hi")}}}
	events, wait := (&AnthropicClient{}).Stream(context.Background(), model, transcript, provider.StreamOptions{}, Auth{APIKey: "k"})
	for range events {
	}
	_, _ = wait()
	if hits.Load() != 1 {
		t.Fatalf("server at ANTHROPIC_BASE_URL got %d requests to /v1/messages, want 1", hits.Load())
	}
}
