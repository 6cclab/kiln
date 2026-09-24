package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/claude/writesettings"
)

type fakeManageGate struct {
	fakeInspectGate
}

func TestManagePermissionsModalCyclesMode(t *testing.T) {
	gate := &fakeManageGate{fakeInspectGate{mode: settings.ModeManual}}
	source := ManageCommands(ManageDeps{Gate: gate})
	res, err := findCmd(t, source, "permissions").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Modal == nil {
		t.Fatal("expected a modal")
	}
	msg, err := res.Modal.Act("m", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "acceptEdits") {
		t.Fatalf("got %q, want the next mode after manual", msg)
	}
	if gate.mode != settings.ModeAcceptEdits {
		t.Fatalf("got %q", gate.mode)
	}
}

func TestManagePermissionsPromoteToDeny(t *testing.T) {
	gate := &fakeManageGate{fakeInspectGate{perms: settings.Permissions{Allow: []string{"Bash(ls)"}}}}
	var saved []string
	source := ManageCommands(ManageDeps{
		Gate: gate,
		SaveRule: func(list writesettings.RuleList, rule string) error {
			saved = append(saved, string(list)+":"+rule)
			return nil
		},
	})
	res, err := findCmd(t, source, "permissions").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := res.Modal.Act("p", "allow:Bash(ls)"); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || saved[0] != "deny:Bash(ls)" {
		t.Fatalf("got %v", saved)
	}
}

func TestManageAgentsShadowsBuiltinAgents(t *testing.T) {
	// Registry precedence: manage's /agents is registered after builtins'
	// /agents in cli.ts's order, so it must win once both sources are in
	// the same registry.
	r := NewRegistry()
	r.Add(BuiltinCommands(BuiltinDeps{Agents: nil}))
	r.Add(ManageCommands(ManageDeps{Gate: &fakeManageGate{}}))

	cmd, ok := r.Get("agents")
	if !ok {
		t.Fatal("expected /agents to resolve")
	}
	res, err := cmd.Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Modal == nil {
		t.Fatal("expected manage's /agents (with a modal) to have won, not builtins'")
	}
}

func TestManageMcpListsToolsAction(t *testing.T) {
	source := ManageCommands(ManageDeps{
		Gate: &fakeManageGate{},
		MCPStatuses: func() []ServerStatus {
			return []ServerStatus{{Name: "grafana", OK: true, ToolCount: 2}}
		},
		MCPTools: func() []MCPTool {
			return []MCPTool{{Name: "query_prometheus", Server: "grafana"}, {Name: "list_dashboards", Server: "grafana"}}
		},
	})
	res, err := findCmd(t, source, "mcp").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := res.Modal.Act("t", "grafana")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "query_prometheus") || !strings.Contains(msg, "list_dashboards") {
		t.Fatalf("got %q", msg)
	}
}
