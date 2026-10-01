package permission

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/andrepato/harness/internal/claude/settings"
)

// Gate-level checks for Read/Edit path rules (settings/pathrules.go): the
// gate must hand Decide the primary working directory and the rules'
// sources, and its own workspace boundary must resolve a path the way the
// tools do.

func writeJSON(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestGate_UserSettingsEditDenyBlocksWrite: a user-settings
// Edit(~/notes/**) deny, loaded through LoadSettings, blocks a write to
// that file even under bypassPermissions and with the home directory added
// as a workspace root.
func TestGate_UserSettingsEditDenyBlocksWrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := t.TempDir()
	writeJSON(t, filepath.Join(home, ".claude", "settings.json"), `{"permissions":{"deny":["Edit(~/notes/**)"]}}`)
	s := settings.LoadSettings(proj, settings.LoadOptions{})

	g := NewGate(GateOptions{Permissions: s.Permissions, Mode: settings.ModeBypassPermissions, Roots: []string{proj, home}})
	for _, arg := range []string{"~/notes/todo.md", filepath.Join(home, "notes", "todo.md")} {
		blocked, err := g.Check(context.Background(), Request{ToolName: "write", PrimaryArg: arg, Args: map[string]any{"path": arg, "content": "x"}})
		if err != nil {
			t.Fatal(err)
		}
		if blocked == nil {
			t.Errorf("write %s was not blocked by the user Edit(~/notes/**) deny", arg)
		}
	}
	other := filepath.Join(proj, "notes", "todo.md")
	if blocked, _ := g.Check(context.Background(), Request{ToolName: "write", PrimaryArg: other, Args: map[string]any{"path": other}}); blocked != nil {
		t.Errorf("write %s (the project's notes, not ~/notes) was blocked: %s", other, blocked.Reason)
	}
}

// TestGate_ProjectRelativeRuleAnchorsAtFirstRoot: a project Read(.env)
// deny anchors at the gate's primary root, not the process's working
// directory, and a relative path argument resolves there too.
func TestGate_ProjectRelativeRuleAnchorsAtFirstRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	proj := t.TempDir()
	writeJSON(t, filepath.Join(proj, ".claude", "settings.json"), `{"permissions":{"deny":["Read(.env)"]}}`)
	s := settings.LoadSettings(proj, settings.LoadOptions{})
	g := NewGate(GateOptions{Permissions: s.Permissions, Mode: settings.ModeAuto, Roots: []string{proj}})

	for _, arg := range []string{"pkg/config/.env", filepath.Join(proj, "pkg", ".env")} {
		blocked, err := g.Check(context.Background(), Request{ToolName: "read", PrimaryArg: arg, Args: map[string]any{"path": arg}})
		if err != nil {
			t.Fatal(err)
		}
		if blocked == nil {
			t.Errorf("read %s was not blocked by Read(.env)", arg)
		}
	}
}

// TestGate_WithinRootsResolvesLikeTheTools: "~/x", "@/x" and file:///x
// name files outside the workspace; the boundary must not read them as
// relative paths under the first root.
func TestGate_WithinRootsResolvesLikeTheTools(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := t.TempDir()
	g := NewGate(GateOptions{Mode: settings.ModeManual, Roots: []string{proj}})
	for _, arg := range []string{"~/.ssh/id_rsa", "@/etc/passwd", "file:///etc/passwd"} {
		if g.WithinRoots(arg) {
			t.Errorf("WithinRoots(%q) = true; the tool opens a file outside %s", arg, proj)
		}
	}
	if !g.WithinRoots("@src/a.go") {
		t.Error(`WithinRoots("@src/a.go") = false; the tool opens <root>/src/a.go`)
	}
	// And so a read of one, which never asks inside the workspace, asks.
	blocked, err := g.Check(context.Background(), Request{ToolName: "read", PrimaryArg: "~/.ssh/id_rsa", Args: map[string]any{"path": "~/.ssh/id_rsa"}})
	if err != nil {
		t.Fatal(err)
	}
	if blocked == nil {
		t.Error("headless read of ~/.ssh/id_rsa proceeded without the outside-workspace question")
	}
}

// TestGate_RemoveRuleKeepsSourcesAligned: removing a rule must drop its
// source too, or every later rule would be judged against the wrong
// anchor.
func TestGate_RemoveRuleKeepsSourcesAligned(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := t.TempDir()
	user := settings.RuleSource{Scope: "user", File: filepath.Join(home, ".claude", "settings.json"), Root: filepath.Join(home, ".claude")}
	project := settings.RuleSource{Scope: "project", File: filepath.Join(proj, ".claude", "settings.json")}
	g := NewGate(GateOptions{
		Permissions: settings.Permissions{
			Deny:     []string{"Read(/a/**)", "Read(/secrets/**)"},
			DenyFrom: []settings.RuleSource{project, user},
		},
		Mode:  settings.ModeAuto,
		Roots: []string{proj},
	})
	g.RemoveRule(RuleDeny, "Read(/a/**)")
	p := g.Permissions()
	if len(p.DenyFrom) != 1 || p.DenyFrom[0] != user {
		t.Fatalf("DenyFrom = %+v, want [user]", p.DenyFrom)
	}
	target := filepath.Join(home, ".claude", "secrets", "k")
	if blocked, _ := g.Check(context.Background(), Request{ToolName: "read", PrimaryArg: target, Args: map[string]any{"path": target}}); blocked == nil {
		t.Error("the user rule lost its ~/.claude anchor after an earlier rule was removed")
	}

	g.AddRule(RuleDeny, "Read(/b/**)") // a session rule: anchored at the primary root
	session := filepath.Join(proj, "b", "k")
	if blocked, _ := g.Check(context.Background(), Request{ToolName: "read", PrimaryArg: session, Args: map[string]any{"path": session}}); blocked == nil {
		t.Error("a session /path rule did not anchor at the primary root")
	}
}
