package harness

import (
	"testing"

	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/tool"
	"github.com/andrepato/harness/internal/tools"
)

// TestBuildStreamOptionsServerToolFallback: web_search backed by SearXNG
// is declared as a plain function tool to a non-Anthropic model (whose
// client would otherwise drop a server tool) and stays Anthropic's server
// tool on the Anthropic Messages API. Without a SearXNG URL it stays a
// server tool everywhere, as before.
func TestBuildStreamOptionsServerToolFallback(t *testing.T) {
	cfg := session.LaneConfiguration{ActiveToolNames: []string{"web_search"}}
	def := func(searxng string, api provider.Api) provider.ToolDef {
		t.Helper()
		opts := Options{Tools: tool.NewSet(tools.WebSearchTool(searxng))}
		got := buildStreamOptions(opts, cfg, api).Tools
		if len(got) != 1 {
			t.Fatalf("tools = %d, want 1", len(got))
		}
		return got[0]
	}

	if d := def("http://searx.local", provider.ApiOpenAICompletions); d.ServerTool != nil || len(d.Parameters) == 0 {
		t.Fatalf("SearXNG web_search on openai-completions: ServerTool=%s, want a function tool", d.ServerTool)
	}
	if d := def("http://searx.local", provider.ApiAnthropicMessages); d.ServerTool == nil {
		t.Fatal("SearXNG web_search on anthropic-messages lost its server-tool declaration")
	}
	if d := def("", provider.ApiOpenAICompletions); d.ServerTool == nil {
		t.Fatal("web_search without SearXNG became a function tool; nothing could run it")
	}
}
