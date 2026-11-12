package cli

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/claude/agents"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/mcp"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tools"
)

func TestCapabilityCatalogTracksRefreshAndScope(t *testing.T) {
	coding, _ := mcp.PostureByName("coding")
	s := &mcpSession{posture: coding, state: mcp.NewGateState(), tier: budget.Tier{ToolStrategy: budget.StrategyPostureIndex}}
	catalog := capabilityCatalog(func() []tools.SkillRecord { return []tools.SkillRecord{{Name: "db", Description: "database"}} }, s)
	if len(catalog().MCP) != 0 {
		t.Fatal("expected empty startup catalog")
	}
	s.setTools([]mcp.McpTool{{Server: "github", Name: "search", QualifiedName: "mcp__github__search"}})
	if len(catalog().MCP) != 1 {
		t.Fatal("catalog did not refresh")
	}
	snapshot := catalog()
	s.setTools(nil)
	if len(snapshot.MCP) != 1 {
		t.Fatal("snapshot mutated")
	}
}

func TestCapabilitySearchBudgetFollowsSessionTier(t *testing.T) {
	s := &mcpSession{tier: budget.Tier{SkillsListingTokens: 256}}
	current := capabilitySearchBudget(claudesettings.SkillDiscovery{}, s)
	if got := current(); got != 256 {
		t.Fatalf("small tier budget = %d", got)
	}
	s.mu.Lock()
	s.tier.SkillsListingTokens = 4096
	s.mu.Unlock()
	if got := current(); got != 1024 {
		t.Fatalf("large tier budget = %d", got)
	}
}

func TestCapabilityCatalogConcurrentRefreshSnapshots(t *testing.T) {
	all, _ := mcp.PostureByName("all")
	s := &mcpSession{posture: all, state: mcp.NewGateState()}
	catalog := capabilityCatalog(func() []tools.SkillRecord { return nil }, s)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			s.setTools([]mcp.McpTool{{Server: "fixture", Name: "echo", QualifiedName: "mcp__fixture__echo"}})
			s.setTools(nil)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			snapshot := catalog()
			if len(snapshot.MCP) > 1 {
				t.Errorf("invalid snapshot: %+v", snapshot.MCP)
			}
		}
	}()
	wg.Wait()
}

func TestCapabilitySuggestionsPinsBoundsAndDoNotAdmit(t *testing.T) {
	all, _ := mcp.PostureByName("all")
	state := mcp.NewGateState()
	catalog := func() tools.CapabilityCatalog {
		return tools.CapabilityCatalog{Skills: []tools.SkillRecord{{Name: "pinned", Description: "Pinned guidance"}, {Name: "database", Description: "SQL migrations"}}, MCP: []mcp.McpTool{{Server: "fixture", Name: "database_metrics", QualifiedName: "mcp__fixture__database_metrics", Description: "Database metrics"}}, Scope: all, State: state}
	}
	input := []msg.Message{msg.UserMessage{Content: msg.Blocks{msg.Text("database")}}}
	config := claudesettings.SkillDiscovery{Pinned: []string{"excluded", "pinned"}}
	got, err := capabilitySuggestionContext(catalog, config, func() int { return 128 })(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	text := msg.TextOf(got[len(got)-1].(msg.UserMessage).Content)
	if !strings.Contains(text, "pinned [skill]") || strings.Contains(text, "excluded") || len(text) > 512 {
		t.Fatalf("suggestion: %q", text)
	}
	if len(state.AdmittedNames()) != 0 {
		t.Fatalf("implicit admission: %v", state.AdmittedNames())
	}
	if msg.TextOf(input[0].(msg.UserMessage).Content) != "database" {
		t.Fatal("history mutated")
	}
	config.Mode = "search"
	noSuggestions, err := capabilitySuggestionContext(catalog, config, func() int { return 128 })(context.Background(), input)
	if err != nil || len(noSuggestions) != len(input) || msg.TextOf(noSuggestions[0].(msg.UserMessage).Content) != "database" {
		t.Fatalf("search mode should be explicit: %v, %v", noSuggestions, err)
	}
}

func TestCapabilitySuggestionObservedReasons(t *testing.T) {
	all, _ := mcp.PostureByName("all")
	catalog := func() tools.CapabilityCatalog {
		return tools.CapabilityCatalog{
			Skills: []tools.SkillRecord{{Name: "pinned", Description: "General guidance"}, {Name: "database", Description: "Database guidance"}},
			Agents: []agents.Definition{{Name: "db-agent", Description: "Database review"}}, Scope: all,
		}
	}
	input := []msg.Message{msg.UserMessage{Content: msg.Blocks{msg.Text("database")}}}
	ledger := newContextSelectionLedger()
	ledger.BeginRequest(input)
	_, err := capabilitySuggestionContextObserved(catalog, claudesettings.SkillDiscovery{Pinned: []string{"pinned"}}, func() int { return 128 }, ledger.Record)(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	got := ledger.SnapshotForRequest(input, "", nil, false)
	for _, want := range []struct{ kind, name, reason string }{
		{"skill", "pinned", "pinned"},
		{"skill", "database", "suggested by metadata match"},
		{"agent", "db-agent", "suggested by metadata match"},
	} {
		found := false
		for _, item := range got {
			if item.Kind == want.kind && item.Name == want.name && item.Reason == want.reason && item.Included {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %+v: %+v", want, got)
		}
	}
}

func TestCapabilitySuggestionsAgentCollisionAndDisabled(t *testing.T) {
	all, _ := mcp.PostureByName("all")
	s := &mcpSession{posture: all, state: mcp.NewGateState()}
	skills := func() []tools.SkillRecord { return []tools.SkillRecord{{Name: "review", Description: "Review code"}} }
	defs := []agents.Definition{{Name: "review", Description: "Review code", Prompt: "private agent instructions"}}
	input := []msg.Message{msg.UserMessage{Content: msg.Blocks{msg.Text("review")}}}
	for _, tc := range []struct {
		name    string
		include bool
		want    bool
	}{
		{name: "included", include: true, want: true},
		{name: "disabled", include: false, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := capabilityCatalogWithAgents(skills, defs, tc.include, s)
			got, err := capabilitySuggestionContext(catalog, claudesettings.SkillDiscovery{Pinned: []string{"review"}}, func() int { return 128 })(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			text := msg.TextOf(got[len(got)-1].(msg.UserMessage).Content)
			if !strings.Contains(text, "review [skill]") || strings.Contains(text, "review [agent]") != tc.want || strings.Contains(text, "private agent instructions") {
				t.Fatalf("agent suggestion: %q", text)
			}
			if len(s.state.AdmittedNames()) != 0 {
				t.Fatalf("suggestion changed MCP admission: %v", s.state.AdmittedNames())
			}
		})
	}
}
