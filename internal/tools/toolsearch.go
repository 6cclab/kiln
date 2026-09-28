package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	mcpgate "github.com/andrepato/harness/internal/mcp"
	"github.com/andrepato/harness/internal/tool"
)

// toolSearchParameters adds an explicit maxResults on top of gating.ts's
// createToolSearch, which only ever exposes `query` to the model and takes
// maxResults as a construction-time option (default 5). Letting the model
// tune how many matches it wants costs nothing extra and needs no new
// machinery, so it is included here even though it is not in the TS tool.
var toolSearchParameters = json.RawMessage(`{
	"type": "object",
	"properties": {
		"query": {"type": "string", "description": "Keywords describing the capability you need."},
		"maxResults": {"type": "integer", "description": "Maximum number of tools to enable.", "default": 5}
	},
	"required": ["query"]
}`)

// toolSearchDefaultMaxResults is gating.ts's createToolSearch default.
const toolSearchDefaultMaxResults = 5

// OnAdmit is called after tool_search admits new tools, so the caller can
// widen the session's active tool set. Mirrors gating.ts's onAdmit
// callback, which cli.ts wires to session.lane.setActiveTools.
type OnAdmit func(ctx context.Context, names []string) error

type toolSearchArgs struct {
	Query      string `json:"query"`
	MaxResults *int   `json:"maxResults"`
}

// ToolSearchTool builds the `tool_search` tool: it searches the MCP tools
// scoped to posture by keyword, admits the top matches into state
// permanently for the session, and returns their full schemas. Mirrors
// gating.ts's createToolSearch.
//
// posture and the searchable set are captured at construction time, same
// as cli.ts's single `createToolSearch({ posture: activePosture, ... })`
// call: a later `/posture` change does not re-scope an already-built
// tool_search, in either implementation.
func ToolSearchTool(tools []mcpgate.McpTool, posture mcpgate.Posture, state *mcpgate.GateState, onAdmit OnAdmit) *tool.Tool {
	var searchable []mcpgate.McpTool
	for _, t := range tools {
		if mcpgate.InPosture(t, posture) {
			searchable = append(searchable, t)
		}
	}

	description := fmt.Sprintf(
		"Search %d available MCP tools by keyword and enable the matches for this session. "+
			"Use this when you need a capability you do not already have a tool for, such as querying Grafana, "+
			"reading secrets, or managing deployments. The matches become callable tools from your next step on.",
		len(searchable),
	)

	return &tool.Tool{
		Name:        "tool_search",
		Label:       "Search tools",
		Description: description,
		Parameters:  toolSearchParameters,
		Execute: func(ctx context.Context, args json.RawMessage, onUpdate tool.Update, inv tool.Invocation) (tool.Result, error) {
			var a toolSearchArgs
			if len(args) > 0 {
				if err := json.Unmarshal(args, &a); err != nil {
					return tool.Result{}, fmt.Errorf("tool_search: decoding arguments: %w", err)
				}
			}
			maxResults := toolSearchDefaultMaxResults
			if a.MaxResults != nil && *a.MaxResults > 0 {
				maxResults = *a.MaxResults
			}

			query := strings.ToLower(a.Query)
			terms := strings.Fields(query)

			type scored struct {
				tool  mcpgate.McpTool
				score int
			}
			var candidates []scored
			for _, t := range searchable {
				sc := mcpgate.Score(t, terms)
				if sc > 0 {
					candidates = append(candidates, scored{tool: t, score: sc})
				}
			}
			// Break ties by name length: with equal relevance the shorter
			// name is the more general tool (search_dashboards over
			// get_dashboard_property), which is usually what was meant.
			sort.SliceStable(candidates, func(i, j int) bool {
				if candidates[i].score != candidates[j].score {
					return candidates[i].score > candidates[j].score
				}
				return len(candidates[i].tool.Name) < len(candidates[j].tool.Name)
			})
			if len(candidates) > maxResults {
				candidates = candidates[:maxResults]
			}

			if len(candidates) == 0 {
				// Name the posture in the failure: "no such tool" and "that
				// tool exists but this posture excludes it" need different
				// fixes.
				text := fmt.Sprintf(
					"No tools matched %q in the %q posture (%d searchable of %d total). "+
						"Switch posture with /posture if the capability belongs to another server.",
					a.Query, posture.Name, len(searchable), len(tools),
				)
				return tool.Text(text), nil
			}

			names := make([]string, len(candidates))
			for i, c := range candidates {
				state.Admit(c.tool.QualifiedName)
				names[i] = c.tool.QualifiedName
			}
			if onAdmit != nil {
				if err := onAdmit(ctx, names); err != nil {
					return tool.Result{}, fmt.Errorf("tool_search: admitting tools: %w", err)
				}
			}

			// The admitted tools join the request's tool list from the
			// next step on, schemas included, so the result only names
			// them: repeating every schema here would keep a second copy
			// in the conversation for the rest of the session.
			var b strings.Builder
			fmt.Fprintf(&b, "Enabled %d tool(s); call them directly:\n", len(candidates))
			for _, c := range candidates {
				fmt.Fprintf(&b, "\n- %s", c.tool.QualifiedName)
				if d := firstSentence(c.tool.Description); d != "" {
					b.WriteString(": " + d)
				}
			}
			return tool.Text(b.String()), nil
		},
	}
}

// firstSentence is the first line of d, cut at its first sentence end and
// at 160 characters: enough to tell matched tools apart.
func firstSentence(d string) string {
	d = strings.TrimSpace(d)
	if i := strings.IndexByte(d, '\n'); i >= 0 {
		d = d[:i]
	}
	if i := strings.Index(d, ". "); i >= 0 {
		d = d[:i+1]
	}
	if r := []rune(d); len(r) > 160 {
		d = string(r[:159]) + "…"
	}
	return d
}
