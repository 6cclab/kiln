package tools

import (
	"context"
	"encoding/json"
	"testing"

	mcpgate "github.com/andrepato/harness/internal/mcp"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
)

func fixtureTools() []mcpgate.McpTool {
	mk := func(name, description string) mcpgate.McpTool {
		return mcpgate.McpTool{
			Server:        "fixture",
			Name:          name,
			QualifiedName: "mcp__fixture__" + name,
			Description:   description,
			InputSchema:   json.RawMessage(`{"type":"object","properties":{}}`),
		}
	}
	return []mcpgate.McpTool{
		mk("echo", "echo back the given text"),
		mk("slow", "sleep for the given number of milliseconds, then return"),
		mk("fail", "always fail with the given message"),
		mk("big", "return N numbered lines, for truncation tests"),
	}
}

func TestToolSearchAdmitsMatchesAndCallsOnAdmit(t *testing.T) {
	tools := fixtureTools()
	all, _ := mcpgate.PostureByName("all")
	state := mcpgate.NewGateState()

	var admittedCalls [][]string
	onAdmit := func(ctx context.Context, names []string) error {
		admittedCalls = append(admittedCalls, names)
		return nil
	}

	search := ToolSearchTool(tools, all, state, onAdmit)
	if search.Name != "tool_search" {
		t.Fatalf("name = %q", search.Name)
	}

	args, _ := json.Marshal(map[string]any{"query": "echo"})
	res, err := search.Execute(context.Background(), args, nil, tool.Invocation{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res.Content)
	}

	if !state.IsAdmitted("mcp__fixture__echo") {
		t.Fatal("expected mcp__fixture__echo to be admitted")
	}
	if len(admittedCalls) != 1 || len(admittedCalls[0]) != 1 || admittedCalls[0][0] != "mcp__fixture__echo" {
		t.Fatalf("onAdmit calls = %+v", admittedCalls)
	}

	text := msg.TextOf(res.Content)
	if text == "" {
		t.Fatal("expected non-empty result text")
	}
}

func TestToolSearchNoMatch(t *testing.T) {
	tools := fixtureTools()
	all, _ := mcpgate.PostureByName("all")
	state := mcpgate.NewGateState()

	search := ToolSearchTool(tools, all, state, nil)
	args, _ := json.Marshal(map[string]any{"query": "nonexistentcapability"})
	res, err := search.Execute(context.Background(), args, nil, tool.Invocation{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(state.AdmittedNames()) != 0 {
		t.Fatalf("expected nothing admitted on no match, got %v", state.AdmittedNames())
	}
	text := msg.TextOf(res.Content)
	if text == "" {
		t.Fatal("expected a not-found message")
	}
}

func TestToolSearchRespectsPosture(t *testing.T) {
	tools := []mcpgate.McpTool{
		{Server: "github", Name: "search", QualifiedName: "mcp__github__search", Description: "search repos"},
		{Server: "grafana", Name: "search_dashboards", QualifiedName: "mcp__grafana__search_dashboards", Description: "search dashboards"},
	}
	coding, _ := mcpgate.PostureByName("coding")
	state := mcpgate.NewGateState()

	search := ToolSearchTool(tools, coding, state, nil)
	args, _ := json.Marshal(map[string]any{"query": "search"})
	_, err := search.Execute(context.Background(), args, nil, tool.Invocation{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !state.IsAdmitted("mcp__github__search") {
		t.Error("expected in-posture github tool to be admitted")
	}
	if state.IsAdmitted("mcp__grafana__search_dashboards") {
		t.Error("expected out-of-posture grafana tool NOT to be admitted")
	}
}

func TestToolSearchMaxResults(t *testing.T) {
	var tools []mcpgate.McpTool
	for i := 0; i < 10; i++ {
		tools = append(tools, mcpgate.McpTool{
			Server: "fixture", Name: "match_tool", QualifiedName: "mcp__fixture__match_tool" + string(rune('a'+i)),
			Description: "matches the query keyword",
		})
	}
	all, _ := mcpgate.PostureByName("all")
	state := mcpgate.NewGateState()
	search := ToolSearchTool(tools, all, state, nil)

	args, _ := json.Marshal(map[string]any{"query": "matches", "maxResults": 2})
	_, err := search.Execute(context.Background(), args, nil, tool.Invocation{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := len(state.AdmittedNames()); got != 2 {
		t.Fatalf("expected maxResults=2 to admit exactly 2, got %d: %v", got, state.AdmittedNames())
	}
}
