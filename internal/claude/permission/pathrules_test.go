package permission

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
	local := settings.RuleSource{Scope: "local", File: filepath.Join(proj, ".claude", "settings.local.json")}
	g := NewGate(GateOptions{
		Permissions: settings.Permissions{
			Deny:     []string{"Read(/a/**)", "Read(/secrets/**)"},
			DenyFrom: []settings.RuleSource{local, user},
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

	g.AddRule(RuleDeny, "Read(/b/**)") // saved to settings.local.json: anchored at the primary root
	session := filepath.Join(proj, "b", "k")
	if blocked, _ := g.Check(context.Background(), Request{ToolName: "read", PrimaryArg: session, Args: map[string]any{"path": session}}); blocked == nil {
		t.Error("an added /path rule did not anchor at the primary root")
	}
}

// TestGate_AddRemoveRuleBySource: AddRule and RemoveRule work on the rule
// and its source together. /permissions saving "Read(/secrets/**)" when user
// settings already has that text adds the project-anchored copy (the user
// one is under ~/.claude); removing it removes only that copy, as
// writesettings.RemoveRule only edits settings.local.json.
func TestGate_AddRemoveRuleBySource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := t.TempDir()
	user := settings.RuleSource{Scope: "user", File: filepath.Join(home, ".claude", "settings.json"), Root: filepath.Join(home, ".claude")}
	project := settings.RuleSource{Scope: "project", File: filepath.Join(proj, ".claude", "settings.json")}
	g := NewGate(GateOptions{
		Permissions: settings.Permissions{
			Deny:     []string{"Read(/secrets/**)", "Read(/p/**)"},
			DenyFrom: []settings.RuleSource{user, project},
		},
		Mode:  settings.ModeAuto,
		Roots: []string{proj},
	})
	check := func(path string) bool {
		blocked, err := g.Check(context.Background(), Request{ToolName: "read", PrimaryArg: path, Args: map[string]any{"path": path}})
		if err != nil {
			t.Fatal(err)
		}
		return blocked != nil
	}
	inProject := filepath.Join(proj, "secrets", "k")
	inClaude := filepath.Join(home, ".claude", "secrets", "k")
	if check(inProject) {
		t.Fatal("the user rule matched in the project before anything was added")
	}

	g.AddRule(RuleDeny, "Read(/secrets/**)")
	if !check(inProject) {
		t.Error("AddRule dropped the project-anchored copy because user settings had the same text")
	}
	if !check(inClaude) {
		t.Error("the user copy stopped applying")
	}

	g.RemoveRule(RuleDeny, "Read(/secrets/**)")
	if check(inProject) {
		t.Error("RemoveRule left the added copy in place")
	}
	if !check(inClaude) {
		t.Error("RemoveRule removed the user settings copy too")
	}
	g.RemoveRule(RuleDeny, "Read(/p/**)")
	if !check(filepath.Join(proj, "p", "x")) {
		t.Error("RemoveRule removed a project settings rule, which stays in its file")
	}
}

// TestGate_SymlinkEscapesWorkspace (review HIGH 1, 2): in acceptEdits, a
// write through a link or a linked directory that resolves outside the
// workspace is outside the workspace: it asks (refused headless), and a
// deny rule on the target blocks it outright.
func TestGate_SymlinkEscapesWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, ".ssh", "authorized_keys"), filepath.Join(proj, "dangle")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, ".ssh"), filepath.Join(proj, "sshdir")); err != nil {
		t.Fatal(err)
	}
	write := func(g *Gate, path string) *BlockResult {
		blocked, err := g.Check(context.Background(), Request{ToolName: "write", PrimaryArg: path, Args: map[string]any{"path": path, "content": "k"}})
		if err != nil {
			t.Fatal(err)
		}
		return blocked
	}

	open := NewGate(GateOptions{Mode: settings.ModeAcceptEdits, Roots: []string{proj}})
	for _, p := range []string{"dangle", "sshdir/authorized_keys"} {
		if open.WithinRoots(p) {
			t.Errorf("WithinRoots(%q) = true; it resolves into ~/.ssh", p)
		}
		if write(open, p) == nil {
			t.Errorf("acceptEdits wrote %s, which resolves outside the workspace, without asking", p)
		}
	}
	if !open.WithinRoots("notes/new.md") {
		t.Error("a new file inside the workspace is not within it")
	}

	denied := NewGate(GateOptions{
		Mode:        settings.ModeAcceptEdits,
		Roots:       []string{proj},
		Permissions: settings.Permissions{Deny: []string{"Edit(~/.ssh/**)"}},
		Prompt: func(context.Context, Request) (PromptChoice, error) {
			return PromptChoice{Kind: PromptAllow}, nil
		},
	})
	for _, p := range []string{"dangle", "sshdir/authorized_keys"} {
		if b := write(denied, p); b == nil || !strings.Contains(b.Reason, "permission rules") {
			t.Errorf("write %s: %+v, want blocked by permission rules", p, b)
		}
	}
}

// TestGate_DotDotLinkLeavesWorkspace (verification HIGH 1): with
// sshl -> ~/.ssh and evil -> "sshl/..", evil/.ssh/authorized_keys is
// ~/.ssh/authorized_keys to the kernel; it is outside the workspace with
// no rules at all, and an Edit(~/.ssh/**) deny blocks it.
func TestGate_DotDotLinkLeavesWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, ".ssh"), filepath.Join(proj, "sshl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sshl/..", filepath.Join(proj, "evil")); err != nil {
		t.Fatal(err)
	}
	p := "evil/.ssh/authorized_keys"
	open := NewGate(GateOptions{Mode: settings.ModeAcceptEdits, Roots: []string{proj}})
	if open.WithinRoots(p) {
		t.Errorf("WithinRoots(%q) = true; it is ~/.ssh/authorized_keys", p)
	}
	denied := NewGate(GateOptions{
		Mode:        settings.ModeAcceptEdits,
		Roots:       []string{proj, home},
		Permissions: settings.Permissions{Deny: []string{"Edit(~/.ssh/**)"}},
	})
	b, err := denied.Check(context.Background(), Request{ToolName: "write", PrimaryArg: p, Args: map[string]any{"path": p, "content": "k"}})
	if err != nil {
		t.Fatal(err)
	}
	if b == nil || !strings.Contains(b.Reason, "permission rules") {
		t.Errorf("write %s: %+v, want blocked by permission rules", p, b)
	}
}

// TestGate_DenyBeatsSessionGrant: an "always allow" answer does not
// outlive a deny or ask rule added afterwards.
func TestGate_DenyBeatsSessionGrant(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	proj := t.TempDir()
	prompts := 0
	g := NewGate(GateOptions{
		Mode:  settings.ModeManual,
		Roots: []string{proj},
		Prompt: func(context.Context, Request) (PromptChoice, error) {
			prompts++
			return PromptChoice{Kind: PromptAllowAlways}, nil
		},
	})
	p := filepath.Join(proj, "notes.md")
	req := Request{ToolName: "write", PrimaryArg: p, Args: map[string]any{"path": p}}
	if blocked, _ := g.Check(context.Background(), req); blocked != nil || prompts != 1 {
		t.Fatalf("first write: blocked=%v prompts=%d", blocked, prompts)
	}

	g.AddRule(RuleAsk, "Edit(notes.md)")
	if _, _ = g.Check(context.Background(), req); prompts != 2 {
		t.Errorf("an ask rule added after the grant did not prompt (prompts=%d)", prompts)
	}
	g.AddRule(RuleDeny, "Edit(notes.md)")
	if blocked, _ := g.Check(context.Background(), req); blocked == nil {
		t.Error("a deny rule added after the grant did not block")
	}
}
