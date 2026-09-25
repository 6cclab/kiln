package mcp_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/budget"
	mcpgate "github.com/andrepato/harness/internal/mcp"
)

func mkTool(server, name, description string) mcpgate.McpTool {
	return mcpgate.McpTool{
		Server:        server,
		Name:          name,
		QualifiedName: "mcp__" + server + "__" + name,
		Description:   description,
	}
}

func TestPostures(t *testing.T) {
	coding, ok := mcpgate.PostureByName("coding")
	if !ok {
		t.Fatal("coding posture not found")
	}
	for _, s := range []string{"infisical", "argocd-mcp", "personal-kb", "homelab-kb", "claude-relay", "sentry", "github"} {
		found := false
		for _, srv := range coding.Servers {
			if srv == s {
				found = true
			}
		}
		if !found {
			t.Errorf("coding posture missing server %q", s)
		}
	}

	ops, ok := mcpgate.PostureByName("ops")
	if !ok {
		t.Fatal("ops posture not found")
	}
	for _, s := range []string{"grafana", "proxmox", "argocd-mcp", "unifi-mcp", "pocket-id", "infisical"} {
		found := false
		for _, srv := range ops.Servers {
			if srv == s {
				found = true
			}
		}
		if !found {
			t.Errorf("ops posture missing server %q", s)
		}
	}

	all, ok := mcpgate.PostureByName("all")
	if !ok || len(all.Servers) != 1 || all.Servers[0] != "*" {
		t.Fatalf("all posture = %+v", all)
	}

	if _, ok := mcpgate.PostureByName("nonexistent"); ok {
		t.Fatal("expected nonexistent posture to be absent")
	}
}

func TestInPosture(t *testing.T) {
	coding, _ := mcpgate.PostureByName("coding")
	all, _ := mcpgate.PostureByName("all")

	inCoding := mkTool("github", "search", "search repos")
	notInCoding := mkTool("grafana", "query", "run a query")

	if !mcpgate.InPosture(inCoding, coding) {
		t.Error("expected github tool in coding posture")
	}
	if mcpgate.InPosture(notInCoding, coding) {
		t.Error("expected grafana tool NOT in coding posture")
	}
	if !mcpgate.InPosture(notInCoding, all) {
		t.Error("expected every tool in the all posture")
	}
}

func TestBuildIndex(t *testing.T) {
	tools := []mcpgate.McpTool{
		mkTool("github", "search", "search repos\nsecond line"),
		mkTool("grafana", "query", strings.Repeat("x", 200)),
	}
	idx := mcpgate.BuildIndex(tools)
	lines := strings.Split(idx, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), idx)
	}
	if lines[0] != "mcp__github__search: search repos" {
		t.Errorf("first-line-only truncation failed: %q", lines[0])
	}
	wantSecond := "mcp__grafana__query: " + strings.Repeat("x", 160)
	if lines[1] != wantSecond {
		t.Errorf("160-char truncation failed: got %d chars of description", len(lines[1])-len("mcp__grafana__query: "))
	}
}

func TestIndexPromptText(t *testing.T) {
	if got := mcpgate.IndexPromptText(nil); got != "" {
		t.Errorf("expected empty index to render empty prompt text, got %q", got)
	}
	got := mcpgate.IndexPromptText([]mcpgate.McpTool{mkTool("github", "search", "search repos")})
	if !strings.Contains(got, "Call tool_search to enable any you need.") {
		t.Errorf("missing tool_search instruction: %q", got)
	}
	if !strings.Contains(got, "<available_tools>\nmcp__github__search: search repos\n</available_tools>") {
		t.Errorf("missing wrapped index: %q", got)
	}
}

func TestScore(t *testing.T) {
	exact := mkTool("grafana", "dashboard", "fetch a dashboard")
	dash := mkTool("grafana", "search_dashboards", "search dashboards by title")
	prop := mkTool("grafana", "get_dashboard_property", "get a dashboard property")
	unrelated := mkTool("grafana", "list_users", "list users")

	terms := []string{"dashboards"}

	// name == term after stemming ("dashboards" -> "dashboard" == "dashboard"): 10 for
	// the name plus 1 for the description also containing the stem.
	if s := mcpgate.Score(exact, terms); s != 11 {
		t.Errorf("exact stem match: got %d, want 11", s)
	}
	// name contains the stem but isn't equal to it: 4 for the name plus 1 for
	// the description.
	if s := mcpgate.Score(dash, terms); s != 5 {
		t.Errorf("substring name match: got %d, want 5", s)
	}
	if s := mcpgate.Score(prop, terms); s != 5 {
		t.Errorf("substring name match: got %d, want 5", s)
	}
	if s := mcpgate.Score(unrelated, terms); s != 0 {
		t.Errorf("no match: got %d, want 0", s)
	}
}

func TestGateStateAdmitOrderAndIdempotence(t *testing.T) {
	s := mcpgate.NewGateState()
	s.Admit("b")
	s.Admit("a")
	s.Admit("b") // re-admit: must not move to the end
	if got := s.AdmittedNames(); len(got) != 2 || got[0] != "b" || got[1] != "a" {
		t.Fatalf("AdmittedNames = %v, want [b a]", got)
	}
	if !s.IsAdmitted("a") || s.IsAdmitted("z") {
		t.Fatal("IsAdmitted incorrect")
	}
}

func TestActiveToolNames(t *testing.T) {
	coding, _ := mcpgate.PostureByName("coding")
	tools := []mcpgate.McpTool{
		mkTool("github", "search", "search repos"),
		mkTool("grafana", "query", "run a query"), // not in coding posture
	}
	resident := []string{"bash", "read"}

	t.Run("full-schemas", func(t *testing.T) {
		state := mcpgate.NewGateState()
		got := mcpgate.ActiveToolNames(tools, coding, budget.StrategyFullSchemas, state, resident)
		want := []string{"bash", "read", "mcp__github__search"}
		if !equalSlices(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("posture-index gated, nothing admitted", func(t *testing.T) {
		state := mcpgate.NewGateState()
		got := mcpgate.ActiveToolNames(tools, coding, budget.StrategyPostureIndex, state, resident)
		want := []string{"bash", "read", "tool_search"}
		if !equalSlices(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("full-index gated, one admitted", func(t *testing.T) {
		state := mcpgate.NewGateState()
		state.Admit("mcp__grafana__query")
		got := mcpgate.ActiveToolNames(tools, coding, budget.StrategyFullIndex, state, resident)
		want := []string{"bash", "read", "tool_search", "mcp__grafana__query"}
		if !equalSlices(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestInPosture_UnknownServerIsInEveryPosture guards the rule that postures
// exclude only servers they know about: a server named by no posture (a
// test fixture, or one the user just added) stays discoverable under
// "coding" and "ops", while a known-but-excluded one (grafana under
// coding) stays out. Break to verify: make InPosture return false for
// unknown servers.
func TestInPosture_UnknownServerIsInEveryPosture(t *testing.T) {
	coding, _ := mcpgate.PostureByName("coding")
	if !mcpgate.InPosture(mcpgate.McpTool{Server: "fixture", Name: "echo"}, coding) {
		t.Error("unknown server fixture should be in the coding posture")
	}
	if mcpgate.InPosture(mcpgate.McpTool{Server: "grafana", Name: "query"}, coding) {
		t.Error("grafana is known and excluded from coding; it must stay out")
	}
	all := mcpgate.Posture{Servers: []string{"*"}}
	if !mcpgate.InPosture(mcpgate.McpTool{Server: "grafana", Name: "query"}, all) {
		t.Error("the all posture must include every server")
	}
}

// benchTools builds n synthetic, structurally realistic mcpgate.McpTool
// values for BenchmarkToolIndexRendering, spread across 9 servers to match
// the real catalog gating.go's doc comment measures (165 tools, 9
// servers).
func benchTools(n int) []mcpgate.McpTool {
	tools := make([]mcpgate.McpTool, n)
	for i := 0; i < n; i++ {
		server := fmt.Sprintf("server-%02d", i%9)
		name := fmt.Sprintf("tool_%03d", i)
		tools[i] = mkTool(server, name, "Does thing "+name+": a realistic one-paragraph description of what this tool does, its inputs, and when to call it, matching the length of real MCP tool descriptions found in production catalogs, running well past the 160-rune truncation this index applies.")
	}
	return tools
}

// BenchmarkToolIndexRendering measures BuildIndex over 165 synthetic
// tools, matching the real catalog size gating.go's doc comment measures
// token counts against.
func BenchmarkToolIndexRendering(b *testing.B) {
	tools := benchTools(165)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mcpgate.BuildIndex(tools)
	}
}
