package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/claude/agents"
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
	// The open panel's "mode" row must follow the change, not keep the
	// mode it was opened with (qa/findings *permissions-mode-row-stale).
	if res.Modal.RefreshHeader == nil {
		t.Fatal("permissions modal has no RefreshHeader")
	}
	if got := res.Modal.RefreshHeader()[0]; !strings.Contains(got, "acceptEdits") {
		t.Errorf("header mode row = %q after cycling, want acceptEdits", got)
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

// TestManageMcpModalCarriesToolsAndMarker replaces the old
// TestManageMcpListsToolsAction (which exercised a generic Act("t", ...)
// mechanism): /mcp's dialog is now Kind: "mcp", rendered by
// internal/tui's dialogMCP, which reads tool names straight off
// Item.Tools rather than through spec.Act — see this file's mcpModal doc
// comment.
func TestManageMcpModalCarriesToolsAndMarker(t *testing.T) {
	source := ManageCommands(ManageDeps{
		Gate: &fakeManageGate{},
		MCPStatuses: func() []ServerStatus {
			return []ServerStatus{
				{Name: "grafana", OK: true, ToolCount: 2},
				{Name: "proxmox", OK: false, Error: "command not found: tsx", Detail: "ENOENT"},
			}
		},
		MCPTools: func() []MCPTool {
			return []MCPTool{{Name: "query_prometheus", Server: "grafana"}, {Name: "list_dashboards", Server: "grafana"}}
		},
		MCPConfigPath: "/Users/andrepato/.claude.json",
	})
	res, err := findCmd(t, source, "mcp").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Modal == nil {
		t.Fatal("expected a modal")
	}
	if res.Modal.Kind != "mcp" {
		t.Fatalf("Kind = %q, want mcp", res.Modal.Kind)
	}
	if len(res.Modal.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(res.Modal.Items))
	}

	byValue := map[string]Item{}
	for _, it := range res.Modal.Items {
		byValue[it.Value] = it
	}

	grafana := byValue["grafana"]
	if grafana.Marker != "✔" {
		t.Errorf("grafana marker = %q, want ✔", grafana.Marker)
	}
	if grafana.Group != "User MCPs (~/.claude.json)" {
		t.Errorf("grafana group = %q", grafana.Group)
	}
	if !strings.Contains(grafana.Description, "2") {
		t.Errorf("grafana description = %q, want it to mention the tool count", grafana.Description)
	}
	if !contains(grafana.Tools, "query_prometheus") || !contains(grafana.Tools, "list_dashboards") {
		t.Errorf("grafana tools = %v", grafana.Tools)
	}

	proxmox := byValue["proxmox"]
	if proxmox.Marker != "✘" {
		t.Errorf("proxmox marker = %q, want ✘", proxmox.Marker)
	}
	if proxmox.Error != "command not found: tsx" {
		t.Errorf("proxmox error = %q", proxmox.Error)
	}
	if proxmox.Detail != "ENOENT" {
		t.Errorf("proxmox detail = %q", proxmox.Detail)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// TestManageMcpModalSectionsByScope: servers are grouped by where they are
// configured, project first (the order they win on a name clash), so a
// server from the repo's .mcp.json is not presented as a user server.
func TestManageMcpModalSectionsByScope(t *testing.T) {
	source := ManageCommands(ManageDeps{
		Gate: &fakeManageGate{},
		Cwd:  "/work/proj",
		MCPStatuses: func() []ServerStatus {
			return []ServerStatus{
				{Name: "grafana", Scope: "user", OK: true},
				{Name: "incidents", Scope: "project", OK: true},
				{Name: "mine", Scope: "local", OK: true},
			}
		},
	})
	res, err := findCmd(t, source, "mcp").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range res.Modal.Items {
		got = append(got, it.Value+" | "+it.Group)
	}
	want := []string{
		"incidents | Project MCPs (/work/proj/.mcp.json)",
		"mine | Local MCPs (~/.claude.json, this project only)",
		"grafana | User MCPs (~/.claude.json)",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("items:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestManageMcpModalListsConnectingServers: /mcp opened while servers are
// still connecting lists them as connecting, not as failed or missing.
func TestManageMcpModalListsConnectingServers(t *testing.T) {
	source := ManageCommands(ManageDeps{
		Gate: &fakeManageGate{},
		MCPStatuses: func() []ServerStatus {
			return []ServerStatus{
				{Name: "grafana", Scope: "user", OK: true, ToolCount: 3},
				{Name: "slow", Scope: "user", Connecting: true},
			}
		},
	})
	res, err := findCmd(t, source, "mcp").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	var slow *Item
	for i := range res.Modal.Items {
		if res.Modal.Items[i].Value == "slow" {
			slow = &res.Modal.Items[i]
		}
	}
	if slow == nil {
		t.Fatalf("connecting server missing from /mcp: %+v", res.Modal.Items)
	}
	if slow.Marker != MarkerConnecting || slow.Description != "connecting…" {
		t.Errorf("connecting server row = marker %q, description %q", slow.Marker, slow.Description)
	}
}

// TestManageAgentsKeepsTheDescriptionsFirstLine: /agents shows an agent's
// whole first description line (the dialog wraps it), not a 60-character
// cut, and leaves out the example dialogues that follow it.
func TestManageAgentsKeepsTheDescriptionsFirstLine(t *testing.T) {
	first := "Reviews code for security issues. Use proactively whenever a task touches authentication, user input, shell commands, or SQL."
	source := ManageCommands(ManageDeps{
		Gate:   &fakeManageGate{},
		Agents: []agents.Definition{{Name: "security-reviewer", Description: first + "\n<example>user: check login</example>"}},
	})
	res, err := findCmd(t, source, "agents").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	got := res.Modal.Items[0].Description
	if !strings.HasSuffix(got, first) {
		t.Errorf("description = %q, want it to end with the whole first line", got)
	}
	if strings.Contains(got, "example") {
		t.Errorf("description = %q carries the examples after the first line", got)
	}
}
