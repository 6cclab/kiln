package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/claude/hooks"
	"github.com/andrepato/harness/internal/claude/settings"
)

type fakeInspectGate struct {
	mode   settings.PermissionMode
	perms  settings.Permissions
	grants []string
	roots  []string
}

func (g *fakeInspectGate) Mode() settings.PermissionMode     { return g.mode }
func (g *fakeInspectGate) Permissions() settings.Permissions { return g.perms }
func (g *fakeInspectGate) SessionGrants() []string           { return g.grants }
func (g *fakeInspectGate) Roots() []string                   { return g.roots }
func (g *fakeInspectGate) SetMode(m settings.PermissionMode) { g.mode = m }

func TestMcpWithNoServersSaysWhereTheyComeFrom(t *testing.T) {
	source := InspectCommands(InspectDeps{})
	res, err := findCmd(t, source, "mcp").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Output) != 1 || !strings.Contains(res.Output[0], "~/.claude.json") {
		t.Fatalf("got %+v", res)
	}
}

func TestMcpListsConnectedAndFailedServers(t *testing.T) {
	source := InspectCommands(InspectDeps{
		MCPStatuses: func() []ServerStatus {
			return []ServerStatus{
				{Name: "grafana", OK: true, ToolCount: 12, Ms: 40},
				{Name: "proxmox", OK: false, Error: "ENOENT"},
			}
		},
	})
	res, err := findCmd(t, source, "mcp").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Output, "\n")
	if !strings.Contains(joined, "1/2 connected") || !strings.Contains(joined, "grafana") || !strings.Contains(joined, "failed: ENOENT") {
		t.Fatalf("got %q", joined)
	}
}

func TestPermissionsListsDenyFirst(t *testing.T) {
	gate := &fakeInspectGate{
		mode: settings.ModeManual,
		perms: settings.Permissions{
			Deny:  []string{"Bash(rm -rf *)"},
			Allow: []string{"Read"},
		},
		roots: []string{"/repo"},
	}
	source := InspectCommands(InspectDeps{Gate: gate})
	res, err := findCmd(t, source, "permissions").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Output, "\n")
	denyIdx := strings.Index(joined, "deny (1)")
	allowIdx := strings.Index(joined, "allow (1)")
	if denyIdx < 0 || allowIdx < 0 || denyIdx > allowIdx {
		t.Fatalf("deny must be listed before allow, got %q", joined)
	}
}

func TestHooksReportsUnfiredEvents(t *testing.T) {
	cfg := hooks.Config{
		hooks.Stop: []hooks.Matcher{{Hooks: []hooks.Command{{Command: "notify-send done"}}}},
	}
	source := InspectCommands(InspectDeps{Hooks: cfg})
	res, err := findCmd(t, source, "hooks").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Output, "\n")
	if !strings.Contains(joined, "not yet fired") {
		t.Fatalf("got %q", joined)
	}
}

func TestDoctorFlagsBypassPermissions(t *testing.T) {
	gate := &fakeInspectGate{mode: settings.ModeBypassPermissions}
	source := InspectCommands(InspectDeps{
		Gate:        gate,
		Tier:        budget.Tier{Name: "medium", ContextWindow: 49152},
		ActiveTools: func() ([]string, error) { return []string{"bash", "read"}, nil },
	})
	res, err := findCmd(t, source, "doctor").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Output, "\n")
	if !strings.Contains(joined, "bypassPermissions") {
		t.Fatalf("got %q", joined)
	}
}

func TestDoctorFlagsNoResidentTools(t *testing.T) {
	source := InspectCommands(InspectDeps{
		ActiveTools: func() ([]string, error) { return nil, nil },
	})
	res, err := findCmd(t, source, "doctor").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Output, "\n")
	if !strings.Contains(joined, "no tools are resident") {
		t.Fatalf("got %q", joined)
	}
}
