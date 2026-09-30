package api

import (
	"testing"

	"github.com/andrepato/harness/internal/msg"
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

// TestCacheRetention_DefaultShort pins that with neither
// HARNESS_CACHE_RETENTION nor CLAUDE_CODE_PROMPT_CACHE_TTL set, kiln asks
// for the short (default, 5m) TTL: an unset cacheRetention that returned
// "long" by mistake would break this. Broken by hardcoding the return to
// "long": TestCacheRetention_DefaultShort and
// TestBuildAnthropicRequest_CacheTTLFromEnv both failed.
func TestCacheRetention_DefaultShort(t *testing.T) {
	if got := cacheRetention(); got != "short" {
		t.Errorf("cacheRetention() with nothing set = %q, want %q", got, "short")
	}
}

// TestBuildAnthropicRequest_CacheTTLFromEnv proves HARNESS_CACHE_RETENTION=long
// puts ttl="1h" on the request's cache_control, unset leaves it "" (the
// API's own 5m default), and Claude Code's own
// CLAUDE_CODE_PROMPT_CACHE_TTL is honored ahead of the harness var. Broken
// by hardcoding retention to "short" in buildAnthropicRequest: this test
// failed on the "long" and "claude code var" cases.
func TestBuildAnthropicRequest_CacheTTLFromEnv(t *testing.T) {
	model := provider.Model{ID: "claude-x", Api: provider.ApiAnthropicMessages}
	transcript := []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}

	cases := []struct {
		name          string
		harnessVar    string
		claudeCodeVar string
		wantTTL       string
	}{
		{name: "unset", wantTTL: ""},
		{name: "harness long", harnessVar: "long", wantTTL: "1h"},
		{name: "harness short explicit", harnessVar: "short", wantTTL: ""},
		{name: "claude code 1h wins", harnessVar: "short", claudeCodeVar: "1h", wantTTL: "1h"},
		{name: "claude code 5m wins", harnessVar: "long", claudeCodeVar: "5m", wantTTL: ""},
		{name: "unrecognized value stays short", harnessVar: "bogus", wantTTL: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HARNESS_CACHE_RETENTION", tc.harnessVar)
			t.Setenv("CLAUDE_CODE_PROMPT_CACHE_TTL", tc.claudeCodeVar)
			req := buildAnthropicRequest(model, transcript, provider.StreamOptions{SystemPrompt: "you are kiln"}, Auth{APIKey: "k"})
			var got string
			for _, sys := range req.System {
				if sys.CacheCtrl != nil {
					got = sys.CacheCtrl.TTL
					break
				}
			}
			if got != tc.wantTTL {
				t.Errorf("system cache_control.ttl = %q, want %q", got, tc.wantTTL)
			}
		})
	}
}
