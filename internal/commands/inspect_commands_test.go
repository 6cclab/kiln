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

func TestHooksListsStopHookWithoutCaveat(t *testing.T) {
	cfg := hooks.Config{
		hooks.Stop: []hooks.Matcher{{Hooks: []hooks.Command{{Command: "notify-send done"}}}},
	}
	source := InspectCommands(InspectDeps{Hooks: cfg})
	res, err := findCmd(t, source, "hooks").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Output, "\n")
	// Stop, SubagentStop, Notification and PreCompact all fire
	// (internal/cli/chat.go, tui.go); /hooks once claimed they did not.
	if !strings.Contains(joined, "Stop\n  notify-send done") || strings.Contains(joined, "fire") {
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
	if !strings.Contains(joined, "no tools are available") {
		t.Fatalf("got %q", joined)
	}
}

// TestDoctorShowsSandboxState checks /doctor's "sandbox" row reflects
// InspectDeps.SandboxLine (cli's sandboxReport) instead of being absent —
// qa/findings/20261004T205021Z-doctor-misses-sandbox-and-hook-events.json:
// nothing in /doctor said whether the sandbox was on.
func TestDoctorShowsSandboxState(t *testing.T) {
	source := InspectCommands(InspectDeps{
		ActiveTools: func() ([]string, error) { return []string{"bash"}, nil },
		SandboxLine: "on (seatbelt), regular permissions, no allowed domains",
	})
	res, err := findCmd(t, source, "doctor").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Output, "\n")
	if !strings.Contains(joined, "sandbox    on (seatbelt)") {
		t.Fatalf("sandbox row missing/wrong, got %q", joined)
	}
}

// TestDoctorFlagsUnsupportedHookEvents checks a settings.json hook
// registered for an event kiln does not fire (PermissionRequest) shows up
// as a named problem, instead of silently vanishing from the hook count —
// qa/findings/20261004T205021Z-doctor-misses-sandbox-and-hook-events.json.
func TestDoctorFlagsUnsupportedHookEvents(t *testing.T) {
	cfg := hooks.Config{
		hooks.PreToolUse:           []hooks.Matcher{{Hooks: []hooks.Command{{Command: "echo hi"}}}},
		hooks.Event("PostCompact"): []hooks.Matcher{{Hooks: []hooks.Command{{Command: "echo pc"}}}},
	}
	source := InspectCommands(InspectDeps{
		Hooks:       cfg,
		ActiveTools: func() ([]string, error) { return []string{"bash"}, nil },
	})
	res, err := findCmd(t, source, "doctor").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Output, "\n")
	if !strings.Contains(joined, "PostCompact") {
		t.Fatalf("expected the unsupported event named in the problem list, got %q", joined)
	}
}
