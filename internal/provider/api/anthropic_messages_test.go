package api

import (
	"testing"

	"github.com/andrepato/harness/internal/provider"
)

// TestAnthropicHeaders_VersionAlwaysSent pins the header the API requires on
// every request, including OAuth ones: a live OAuth request without it was
// rejected with 400 "anthropic-version: header is required".
func TestAnthropicHeaders_VersionAlwaysSent(t *testing.T) {
	model := provider.Model{ID: "claude-x", Api: provider.ApiAnthropicMessages}
	for _, oauth := range []bool{false, true} {
		h := anthropicHeaders(model, Auth{APIKey: "k", IsOAuth: oauth}, false)
		if got := h.Get("anthropic-version"); got != anthropicVersion {
			t.Errorf("IsOAuth=%v: anthropic-version = %q, want %q", oauth, got, anthropicVersion)
		}
		if oauth && h.Get("Authorization") == "" {
			t.Errorf("IsOAuth: Authorization header missing")
		}
		if !oauth && h.Get("x-api-key") == "" {
			t.Errorf("api key: x-api-key header missing")
		}
	}
}
