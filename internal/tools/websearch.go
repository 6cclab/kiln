package tools

import (
	"context"
	"encoding/json"

	"github.com/andrepato/harness/internal/tool"
)

// web_search — a provider server tool, meaningful only when the active
// model is served over the Anthropic Messages API. Anthropic executes the
// search on its own infrastructure: the request declares the tool with
// webSearchServerToolJSON below, and the response stream carries the
// query and results back as server_tool_use / web_search_tool_result
// content blocks, which the Anthropic client (internal/provider/api/
// anthropic_messages.go) turns into msg.ProviderBlock entries rather than
// a msg.ToolCall. This tool's Execute is therefore never legitimately
// invoked: a provider that actually offers web_search always resolves the
// call itself before the harness turn loop ever sees a client-side call
// for it (see tool.Tool.ServerTool's doc comment), and every other
// provider's request builder omits the tool entirely (it never appears in
// opts.Tools' declared schema for them), so their models cannot call it
// either.
//
// Registered as a resident tool (internal/cli/mcp.go's residentAll) so it
// is always in the tool set kept for an Anthropic session; chat.go leaves
// it out of the active tool set for a session whose permission settings
// carry a deny rule for "WebSearch" (Claude Code's name for it), which is
// what keeps it from being declared even to Anthropic in that case.

// webSearchServerToolJSON is the Anthropic tool-declaration block sent
// verbatim in place of a function schema. max_uses caps how many searches
// one turn may run, matching Anthropic's own documented default.
var webSearchServerToolJSON = json.RawMessage(`{"type":"web_search_20250305","name":"web_search","max_uses":5}`)

const webSearchDescription = "Search the web. Executed server-side by Anthropic; only available when the " +
	"active model is served over the Anthropic Messages API."

// WebSearchTool builds the web_search resident tool.
func WebSearchTool() *tool.Tool {
	return &tool.Tool{
		Name:        "web_search",
		Label:       "web search",
		Description: webSearchDescription,
		Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
		ServerTool:  webSearchServerToolJSON,
		Execute: func(ctx context.Context, args json.RawMessage, onUpdate tool.Update, inv tool.Invocation) (tool.Result, error) {
			// Never legitimately reached -- see the package doc comment
			// above. Defensive only: refuse rather than pretend to search.
			return tool.Errorf("web_search is executed server-side by Anthropic and cannot be run directly"), nil
		},
	}
}
