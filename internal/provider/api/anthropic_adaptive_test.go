package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/provider/catalog"
)

// TestAdaptiveThinkingModelsGetEffortNotBudgets: models the catalog marks
// forceAdaptiveThinking get thinking "adaptive" plus output_config.effort.
// Opus 5.5 rejects "disabled" (req_011CfW6VDzwAzRAgoSscp8x9), so "off"
// becomes its lowest supported effort; Opus 4.8 can still be disabled.
func TestAdaptiveThinkingModelsGetEffortNotBudgets(t *testing.T) {
	opus55, ok := catalog.Lookup("anthropic", "claude-opus-5-5")
	if !ok {
		t.Fatal("claude-opus-5-5 not in the catalog")
	}
	opus48, ok := catalog.Lookup("anthropic", "claude-opus-4-8")
	if !ok {
		t.Fatal("claude-opus-4-8 not in the catalog")
	}
	user := []msg.Message{msg.UserMessage{Content: msg.Blocks{msg.Text("hi")}}}
	cases := []struct {
		model provider.Model
		level provider.ThinkingLevel
		want  string
	}{
		{opus55, provider.ThinkingOff, `"thinking":{"type":"adaptive"},"output_config":{"effort":"low"}`},
		{opus55, provider.ThinkingMinimal, `"thinking":{"type":"adaptive"},"output_config":{"effort":"low"}`},
		{opus55, provider.ThinkingMedium, `"thinking":{"type":"adaptive"},"output_config":{"effort":"medium"}`},
		{opus55, provider.ThinkingXHigh, `"thinking":{"type":"adaptive"},"output_config":{"effort":"xhigh"}`},
		{opus48, provider.ThinkingOff, `"thinking":{"type":"disabled"}`},
		{opus48, provider.ThinkingHigh, `"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}`},
	}
	for _, c := range cases {
		body, _ := json.Marshal(buildAnthropicRequest(c.model, user, provider.StreamOptions{ThinkingLevel: c.level}, Auth{}))
		if !strings.Contains(string(body), c.want) {
			t.Errorf("%s at %q: request has no %s:\n%s", c.model.ID, c.level, c.want, body)
		}
		if strings.Contains(string(body), "budget_tokens") {
			t.Errorf("%s at %q: sends a thinking budget", c.model.ID, c.level)
		}
	}
	// No level asked for: adaptive, with the effort left to the model.
	body, _ := json.Marshal(buildAnthropicRequest(opus55, user, provider.StreamOptions{}, Auth{}))
	if !strings.Contains(string(body), `"thinking":{"type":"adaptive"}`) || strings.Contains(string(body), "output_config") {
		t.Errorf("no level asked for: want adaptive thinking with no effort pinned:\n%s", body)
	}
}
