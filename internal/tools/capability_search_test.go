package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/claude/agents"
	"github.com/andrepato/harness/internal/mcp"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
)

func TestSearchCapabilitiesMixedRankingAndScope(t *testing.T) {
	c := CapabilityCatalog{
		Skills: []SkillRecord{{Name: "database", Description: "SQL migrations", Body: "private instructions"}, {Name: "reports", Description: "Read database dashboards"}},
		MCP: []mcp.McpTool{
			{Server: "github", Name: "database_search", QualifiedName: "mcp__github__database_search", Description: "Search database repositories"},
			{Server: "grafana", Name: "database_metrics", QualifiedName: "mcp__grafana__database_metrics", Description: "Read database metrics"},
		},
	}
	for _, tc := range []struct {
		posture string
		want    int
	}{
		{posture: "coding", want: 3},
		{posture: "all", want: 4},
	} {
		t.Run(tc.posture, func(t *testing.T) {
			c.Scope, _ = mcp.PostureByName(tc.posture)
			hits := SearchCapabilities(c, "database", 5)
			if len(hits) != tc.want || hits[0].Name != "database" || hits[1].Name != "mcp__github__database_search" {
				t.Fatalf("rank/scope: %+v", hits)
			}
			for _, h := range hits {
				if strings.Contains(h.Description, "private instructions") || (tc.posture == "coding" && h.Name == "mcp__grafana__database_metrics") {
					t.Fatalf("leaked body or excluded MCP: %+v", h)
				}
			}
		})
	}
}

func TestSearchCapabilitiesUsesEffectiveSkillMetadata(t *testing.T) {
	c := CapabilityCatalog{Skills: []SkillRecord{
		{Name: "deploy", Description: "old cloud deployment"},
		{Name: "deploy", Description: "new database deployment"},
	}}
	hits := SearchCapabilities(c, "deploy", 5)
	if len(hits) != 1 || hits[0].Description != "new database deployment" {
		t.Fatalf("search exposed shadowed command: %+v", hits)
	}
}

func TestCapabilitySearchRefreshAdmissionAndBound(t *testing.T) {
	all, _ := mcp.PostureByName("all")
	state := mcp.NewGateState()
	var live []mcp.McpTool
	var admitted [][]string
	search := CapabilitySearchTool(func() CapabilityCatalog {
		return CapabilityCatalog{Skills: []SkillRecord{{Name: "sql", Description: "SQL help", Body: "secret body"}}, MCP: live, Scope: all, State: state}
	}, 80, func(_ context.Context, names []string) error {
		admitted = append(admitted, append([]string(nil), names...))
		return nil
	})
	call := func(query string) string {
		t.Helper()
		args, err := json.Marshal(map[string]any{"query": query})
		if err != nil {
			t.Fatal(err)
		}
		result, err := search.Execute(context.Background(), args, nil, tool.Invocation{})
		if err != nil {
			t.Fatal(err)
		}
		return msg.TextOf(result.Content)
	}
	if got := call("metrics"); !strings.Contains(got, "No matching") {
		t.Fatal(got)
	}
	live = []mcp.McpTool{{Server: "fixture", Name: "metrics", QualifiedName: "mcp__fixture__metrics", Description: "Read metrics", InputSchema: json.RawMessage(`{"secret":"schema"}`)}, {Server: "fixture", Name: "unrelated", QualifiedName: "mcp__fixture__unrelated", Description: "Other work"}}
	got := call("metrics")
	if !strings.Contains(got, "call mcp__fixture__metrics") || strings.Contains(got, "schema") || len(got) > 320 {
		t.Fatalf("result: %q", got)
	}
	if !state.IsAdmitted("mcp__fixture__metrics") || state.IsAdmitted("mcp__fixture__unrelated") || len(admitted) != 1 || len(admitted[0]) != 1 {
		t.Fatalf("admission: %v, calls: %v", state.AdmittedNames(), admitted)
	}
	if got := call("sql"); strings.Contains(got, "secret body") || len(admitted) != 1 {
		t.Fatalf("skill result/admission: %q, %v", got, admitted)
	}
}

func TestCapabilitySearchNoAdmissionWhenResultDoesNotFit(t *testing.T) {
	all, _ := mcp.PostureByName("all")
	state := mcp.NewGateState()
	search := CapabilitySearchTool(func() CapabilityCatalog {
		return CapabilityCatalog{MCP: []mcp.McpTool{{Server: "fixture", Name: "long", QualifiedName: "mcp__fixture__long", Description: "Long description"}}, Scope: all, State: state}
	}, 2, nil)
	res, err := search.Execute(context.Background(), json.RawMessage(`{"query":"long"}`), nil, tool.Invocation{})
	if err != nil {
		t.Fatal(err)
	}
	if state.IsAdmitted("mcp__fixture__long") || strings.Contains(msg.TextOf(res.Content), "mcp__fixture__long") {
		t.Fatalf("admitted unreported tool: %+v", res)
	}
}

func TestCapabilitySearchObservedReasonsFollowReturnedBytes(t *testing.T) {
	all, _ := mcp.PostureByName("all")
	state := mcp.NewGateState()
	var got []struct {
		callID, kind, name, reason string
		included                   bool
	}
	allowance := 2
	search := CapabilitySearchToolObserved(func() CapabilityCatalog {
		return CapabilityCatalog{MCP: []mcp.McpTool{{Server: "fixture", Name: "find", QualifiedName: "mcp__fixture__find", Description: "Find records"}}, Scope: all, State: state}
	}, func() int { return allowance }, nil, func(callID, kind, name, reason string, included bool) {
		got = append(got, struct {
			callID, kind, name, reason string
			included                   bool
		}{callID, kind, name, reason, included})
	})
	args := json.RawMessage(`{"query":"find"}`)
	if _, err := search.Execute(context.Background(), args, nil, tool.Invocation{ToolCallID: "q1"}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].reason != "budget-dropped" || got[0].included || state.IsAdmitted("mcp__fixture__find") {
		t.Fatalf("budget result: %+v", got)
	}
	allowance = 128
	if _, err := search.Execute(context.Background(), args, nil, tool.Invocation{ToolCallID: "q2"}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].callID != "q1" || got[1].callID != "q2" || got[1].reason != "search hit" || !got[1].included || !state.IsAdmitted("mcp__fixture__find") {
		t.Fatalf("search result: %+v", got)
	}
}

func TestCapabilitySearchDynamicBudget(t *testing.T) {
	all, _ := mcp.PostureByName("all")
	state := mcp.NewGateState()
	allowance := 2
	search := CapabilitySearchToolDynamic(func() CapabilityCatalog {
		return CapabilityCatalog{MCP: []mcp.McpTool{{Server: "fixture", Name: "metrics", QualifiedName: "mcp__fixture__metrics", Description: "Read metrics"}}, Scope: all, State: state}
	}, func() int { return allowance }, nil)
	args := json.RawMessage(`{"query":"metrics"}`)
	first, err := search.Execute(context.Background(), args, nil, tool.Invocation{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.TextOf(first.Content)) > allowance*4 || state.IsAdmitted("mcp__fixture__metrics") {
		t.Fatalf("small tier admitted an unreported tool: %+v", first)
	}
	allowance = 128
	second, err := search.Execute(context.Background(), args, nil, tool.Invocation{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg.TextOf(second.Content), "mcp__fixture__metrics") || !state.IsAdmitted("mcp__fixture__metrics") {
		t.Fatalf("new tier budget was ignored: %+v", second)
	}
}

func TestCapabilitySearchAgentActionAndNameCollision(t *testing.T) {
	all, _ := mcp.PostureByName("all")
	c := CapabilityCatalog{
		Skills: []SkillRecord{{Name: "review", Description: "Review code", Body: "private skill body"}},
		Agents: []agents.Definition{{Name: "review", Description: "Review code", Prompt: "private agent prompt"}, {Name: "review", Description: "Review second agent", Prompt: "second private prompt"}},
		MCP:    []mcp.McpTool{{Server: "fixture", Name: "review", QualifiedName: "mcp__fixture__review", Description: "Review code", InputSchema: json.RawMessage(`{"secret":"schema"}`)}},
		Scope:  all, State: mcp.NewGateState(),
	}
	hits := SearchCapabilities(c, "review", 5)
	if len(hits) != 3 || hits[0].Kind != "mcp" || hits[1].Kind != "agent" || hits[2].Kind != "skill" {
		t.Fatalf("cross-kind collisions lost or unstable: %+v", hits)
	}
	for _, h := range hits {
		if strings.Contains(h.Description, "private") || strings.Contains(h.Description, "second agent") || strings.Contains(h.Action, "private") {
			t.Fatalf("body or prompt leaked: %+v", h)
		}
		if h.Kind != "agent" {
			continue
		}
		if !strings.HasPrefix(h.Action, "task(") || !strings.HasSuffix(h.Action, ")") {
			t.Fatalf("invalid task action: %q", h.Action)
		}
		var args struct {
			SubagentType string `json:"subagent_type"`
			Description  string `json:"description"`
			Prompt       string `json:"prompt"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(h.Action, "task("), ")")), &args); err != nil {
			t.Fatal(err)
		}
		if args.SubagentType != "review" || args.Description == "" || args.Prompt == "" {
			t.Fatalf("task args: %+v", args)
		}
	}
}

func TestCapabilityActionsUseJSONEscaping(t *testing.T) {
	name := "review\x01\"\\agent"
	all, _ := mcp.PostureByName("all")
	hits := SearchCapabilities(CapabilityCatalog{
		Skills: []SkillRecord{{Name: name, Description: "review"}},
		Agents: []agents.Definition{{Name: name, Description: "review"}},
		Scope:  all,
	}, "review", 5)
	if len(hits) != 2 {
		t.Fatalf("hits: %+v", hits)
	}
	for _, h := range hits {
		open := strings.IndexByte(h.Action, '(')
		if open < 0 || !strings.HasSuffix(h.Action, ")") {
			t.Fatalf("action: %q", h.Action)
		}
		var args map[string]string
		if err := json.Unmarshal([]byte(h.Action[open+1:len(h.Action)-1]), &args); err != nil {
			t.Fatalf("invalid JSON action %q: %v", h.Action, err)
		}
		key := "skill"
		if h.Kind == "agent" {
			key = "subagent_type"
		}
		if args[key] != name {
			t.Fatalf("action lost name: %+v", args)
		}
	}
}
