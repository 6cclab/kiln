package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/budget"
)

func TestUsageReportsContextBudgetNotPlanLimits(t *testing.T) {
	source := AccountCommands(AccountDeps{
		Tier:       budget.Tier{Name: "medium", ContextWindow: 49152, ToolOutputTokens: 4096},
		ModelLabel: "ollama/qwen3-cc:latest",
	})
	res, err := findCmd(t, source, "usage").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Output, "\n")
	if !strings.Contains(joined, "No plan limits apply") {
		t.Fatalf("got %q", joined)
	}
}

func TestUsageReportsUsedPercent(t *testing.T) {
	source := AccountCommands(AccountDeps{
		Tier:        budget.Tier{Name: "medium", ContextWindow: 49152, Compaction: budget.CompactionSettings{ReserveTokens: 1000}},
		ContextUsed: func() (int, bool) { return 10000, true },
	})
	res, err := findCmd(t, source, "usage").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Output, "\n")
	if !strings.Contains(joined, "used") || !strings.Contains(joined, "%") {
		t.Fatalf("got %q", joined)
	}
}

func TestLoginWithNoWantedProviderSaysNotLoggedIn(t *testing.T) {
	source := AccountCommands(AccountDeps{})
	res, err := findCmd(t, source, "login").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output[0], "Not logged in") {
		t.Fatalf("got %+v", res)
	}
}

func TestLoginWithProviderPointsAtTheCLI(t *testing.T) {
	source := AccountCommands(AccountDeps{})
	res, err := findCmd(t, source, "login").Run(context.Background(), "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(res.Output, "\n"), "kiln login anthropic") {
		t.Fatalf("got %+v", res)
	}
}

func TestTodosCommandRendersMarks(t *testing.T) {
	store := agent.NewTodoStore()
	store.Set([]agent.TodoItem{
		{Content: "write tests", Status: agent.TodoCompleted},
		{Content: "ship it", Status: agent.TodoInProgress},
		{Content: "celebrate", Status: agent.TodoPending},
	})
	source := AccountCommands(AccountDeps{Todos: store})
	res, err := findCmd(t, source, "todos").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Output, "\n")
	if !strings.Contains(joined, "[x] write tests") || !strings.Contains(joined, "[~] ship it") || !strings.Contains(joined, "[ ] celebrate") {
		t.Fatalf("got %q", joined)
	}
}

func TestNoVimStatuslinePlugin(t *testing.T) {
	source := AccountCommands(AccountDeps{})
	cmds, err := source.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cmds {
		if c.Name == "vim" || c.Name == "statusline" || c.Name == "plugin" {
			t.Fatalf("expected %s to be absent, not a stub", c.Name)
		}
	}
}
