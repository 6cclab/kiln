package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/claude/permission"
	"github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/mcp"
	"github.com/andrepato/harness/internal/mods"
	"github.com/andrepato/harness/internal/tool"
)

func TestModsProcessCommandIsTheShellLineTheGateReads(t *testing.T) {
	for _, c := range []struct {
		argv []string
		cwd  string
		env  map[string]string
		want string
	}{
		{[]string{"git", "status"}, "/w", nil, "git status"},
		{[]string{"echo", "a b", "it's"}, "/w", nil, `echo 'a b' 'it'\''s'`},
		{[]string{"make"}, "/w/build", map[string]string{"B": "2", "A": "x y"}, "cd /w/build && A='x y' B=2 make"},
		{[]string{"sh", "-c", "rm -rf /"}, "", nil, "sh -c 'rm -rf /'"},
		{[]string{"echo", ""}, "", nil, "echo ''"},
	} {
		if got := modsProcessCommand(c.argv, c.cwd, "/w", c.env); got != c.want {
			t.Errorf("modsProcessCommand(%q, %q, %v) = %q, want %q", c.argv, c.cwd, c.env, got, c.want)
		}
	}
}

func TestGateModProcessJudgesItAsABashCall(t *testing.T) {
	set := tool.NewSet()
	set.Add(&tool.Tool{Name: "bash"})
	gate := permission.NewGate(permission.GateOptions{
		Mode:        settings.ModeManual,
		Roots:       []string{t.TempDir()},
		Permissions: settings.Permissions{Allow: []string{"Bash(echo *)"}, Deny: []string{"Bash(rm *)"}},
	})
	ctx := context.Background()
	ask := func(set *tool.Set, gate *permission.Gate, sandboxed bool, argv ...string) string {
		t.Helper()
		deny, err := gateModProcess(ctx, set, gate, nil, sandboxed, "p", argv, "", "/w", nil)
		if err != nil {
			t.Fatal(err)
		}
		return deny
	}
	if deny := ask(set, gate, false, "echo", "hi"); deny != "" {
		t.Errorf("an allow-listed command was refused: %q", deny)
	}
	if deny := ask(set, gate, false, "rm", "-rf", "x"); deny == "" {
		t.Error("a deny-listed command was allowed")
	}
	// No rule, no prompter: the gate cannot ask, so it refuses.
	if deny := ask(set, gate, false, "curl", "example.com"); !strings.Contains(deny, "confirmation") {
		t.Errorf("an unlisted command with nobody to ask: %q, want a refusal", deny)
	}
	if deny := ask(set, gate, true, "echo", "hi"); !strings.Contains(deny, "sandbox") {
		t.Errorf("with the OS sandbox on: %q, want a refusal naming it", deny)
	}
	if deny := ask(set, nil, false, "echo", "hi"); !strings.Contains(deny, "gate") {
		t.Errorf("without a gate: %q, want a refusal", deny)
	}
	if deny := ask(tool.NewSet(), gate, false, "echo", "hi"); !strings.Contains(deny, "Bash") {
		t.Errorf("without a Bash tool: %q, want a refusal", deny)
	}
}

func TestSpawnServiceRefusesWithoutAHarness(t *testing.T) {
	var s mods.Services
	modsToolDeps{gate: permission.NewGate(permission.GateOptions{Mode: settings.ModeManual})}.fill(&s)
	deny, err := s.GateProcess(context.Background(), "p", []string{"echo"}, "", nil)
	if err != nil || deny == "" {
		t.Errorf("GateProcess with no tools = %q, %v; want a refusal", deny, err)
	}
}

func TestModsMcpConnectReadsWhatTheHubConnected(t *testing.T) {
	statuses := []mcp.ServerStatus{
		{Name: "plugin_p_up", OK: true},
		{Name: "plugin_p_down", OK: false, Error: "command not found: x"},
		{Name: "plugin_q_up", OK: true},
	}
	pending := []mcp.ServerStatus{{Name: "plugin_p_slow"}}
	for server, want := range map[string]mods.McpConnectAnswer{
		"up":    {Connected: true, Server: "plugin_p_up"},
		"down":  {Reason: "failed"},
		"slow":  {Reason: "failed"},
		"ghost": {Reason: "unlisted"},
	} {
		got := modsMcpConnect(statuses, pending, "p", server)
		if got.Connected != want.Connected || got.Server != want.Server || got.Reason != want.Reason || (!got.Connected && got.Message == "") {
			t.Errorf("modsMcpConnect(%q) = %+v, want %+v with a message", server, got, want)
		}
	}
	var s mods.Services
	modsToolDeps{hub: mcp.NewHub()}.fill(&s)
	if got := s.McpConnect(context.Background(), "p", "up"); got.Reason != "unlisted" {
		t.Errorf("an empty hub answered %+v, want unlisted", got)
	}
	modsToolDeps{}.fill(&s)
	if got := s.McpConnect(context.Background(), "p", "up"); got.Connected || got.Message == "" {
		t.Errorf("no hub answered %+v, want a refusal", got)
	}
}
