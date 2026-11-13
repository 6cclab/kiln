package tools

import (
	"testing"

	"github.com/andrepato/harness/internal/mcp"
)

// TestSuggestCapabilitiesRelevance replays the ynm session's two noisy
// requests: kiln suggested capabilities for "Ok go ahead" ("ok" matched
// inside "token", "go" inside "good") and for an export request (a
// provisioning tool on "file" and "export" alone). A request that really
// is about a capability still gets it.
func TestSuggestCapabilitiesRelevance(t *testing.T) {
	all, _ := mcp.PostureByName("all")
	c := CapabilityCatalog{
		MCP: []mcp.McpTool{
			{Server: "bifrost", Name: "grafana-get_current_oncall_users", QualifiedName: "mcp__bifrost__grafana-get_current_oncall_users",
				Description: "Get the users currently on call, good for paging. Needs an API token."},
			{Server: "bifrost", Name: "grafana-validate_provisioning_file", QualifiedName: "mcp__bifrost__grafana-validate_provisioning_file",
				Description: "Validate a Grafana provisioning file before you export it."},
			{Server: "bifrost", Name: "grafana-search_dashboards", QualifiedName: "mcp__bifrost__grafana-search_dashboards",
				Description: "Search Grafana dashboards by query."},
		},
		Skills: []SkillRecord{{Name: "go-to-market", Description: "Plan a launch, okay for any product."}},
		Scope:  all,
	}
	names := func(hits []CapabilityHit) []string {
		var out []string
		for _, h := range hits {
			out = append(out, h.Name)
		}
		return out
	}
	for _, q := range []string{"Ok go ahead", "ok", "sounds good, continue", "yes please"} {
		if hits := SuggestCapabilities(c, q, 5); len(hits) != 0 {
			t.Errorf("%q suggested %v, want nothing", q, names(hits))
		}
		if got := SuggestSkills(c.Skills, q, 5); len(got) != 0 {
			t.Errorf("%q suggested skills %v, want nothing", q, got)
		}
	}
	export := "Export this session transcript to a file on my desktop when you're code complete"
	if hits := SuggestCapabilities(c, export, 5); len(hits) != 0 {
		t.Errorf("export request suggested %v, want nothing", names(hits))
	}
	hits := SuggestCapabilities(c, "query grafana dashboards for latency", 5)
	if len(hits) == 0 || hits[0].Name != "mcp__bifrost__grafana-search_dashboards" {
		t.Errorf("grafana request suggested %v, want search_dashboards first", names(hits))
	}
	if hits := SuggestCapabilities(c, "dashboards", 5); len(hits) != 1 {
		t.Errorf("one-word request suggested %v, want search_dashboards", names(hits))
	}
	// The search tool the model calls on purpose keeps its broad matching.
	if hits := SearchCapabilities(c, "ok", 5); len(hits) == 0 {
		t.Error("capability_search lost its substring matching")
	}
}
