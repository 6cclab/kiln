package permission

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/claude/settings"
)

// protectedFixture is a workspace with a real .git/config, a symlink to it
// under a harmless name, and a scratch HOME added as a second root (the
// --add-dir ~ case), so ~/.zshrc is inside the workspace.
type protectedFixture struct {
	root, home string
}

func newProtectedFixture(t *testing.T) protectedFixture {
	t.Helper()
	root, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[core]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, ".git", "config"), filepath.Join(root, "settings.ini")); err != nil {
		t.Skipf("cannot make a symlink here: %v", err)
	}
	return protectedFixture{root: root, home: home}
}

func (f protectedFixture) gate(mode settings.PermissionMode, perms settings.Permissions, c Classifier) *Gate {
	return NewGate(GateOptions{Permissions: perms, Mode: mode, Roots: []string{f.root, f.home}, Classifier: c})
}

// protectedTargets are the paths every mode must treat as protected, as
// the edit tool's path argument names them.
func (f protectedFixture) protectedTargets() map[string]string {
	return map[string]string{
		".git/config":           filepath.Join(f.root, ".git", "config"),
		".git/hooks/x":          filepath.Join(f.root, ".git", "hooks", "x"),
		".claude/settings.json": filepath.Join(f.root, ".claude", "settings.json"),
		".mcp.json":             filepath.Join(f.root, ".mcp.json"),
		".vscode/tasks.json":    filepath.Join(f.root, ".vscode", "tasks.json"),
		".kiln/settings":        filepath.Join(f.root, ".kiln", "settings.local.json"),
		"~/.zshrc":              "~/.zshrc",
		"home .zshrc":           filepath.Join(f.home, ".zshrc"),
		"link to .git/config":   filepath.Join(f.root, "settings.ini"),
		".GIT/Config":           filepath.Join(f.root, ".GIT", "Config"),
		".Claude/Settings.JSON": filepath.Join(f.root, ".Claude", "Settings.JSON"),
		"relative .git/config":  ".git/config",
	}
}

func editReq(path string) Request {
	return Request{ToolName: "edit", PrimaryArg: path, Args: map[string]any{"path": path, "old_string": "a", "new_string": "b"}}
}

// The bug this guards: in acceptEdits (and under an Edit allow rule in
// manual or dontAsk) a file-tool write to .git/config, .claude/settings.json
// and the rest went through with no prompt. Every mode except bypass now
// stops for them, before allow rules and acceptEdits, as Claude Code's
// safety check does.
func TestProtectedWrites_AskInManualAndAcceptEdits(t *testing.T) {
	f := newProtectedFixture(t)
	for name, path := range f.protectedTargets() {
		for _, mode := range []settings.PermissionMode{settings.ModeManual, settings.ModeAcceptEdits} {
			for _, perms := range []settings.Permissions{{}, {Allow: []string{"Edit", "Write", "Edit(/**)"}}} {
				for _, tool := range []string{"edit", "write"} {
					g := f.gate(mode, perms, nil)
					p := &promptRecorder{kind: PromptDeny}
					g.SetPrompter(p.prompt)
					req := editReq(path)
					req.ToolName = tool
					blocked, out, err := g.CheckWithOutcome(context.Background(), req)
					if err != nil {
						t.Fatal(err)
					}
					if len(p.reqs) != 1 || blocked == nil || out != OutcomeDeclined {
						t.Errorf("%s %s in %s (allow %v): prompts=%d blocked=%v out=%q, want one prompt", tool, name, mode, perms.Allow, len(p.reqs), blocked != nil, out)
						continue
					}
					if got := p.reqs[0]; got.Grantable || !strings.Contains(got.AutoModeNote, "protected path") {
						t.Errorf("%s %s in %s: prompt grantable=%v note=%q, want no \"don't ask again\" and a protected-path note", tool, name, mode, got.Grantable, got.AutoModeNote)
					}
				}
			}
		}
	}
}

// Approving the prompt lets the write through, once: the next identical
// call asks again, since no grant covers a protected path.
func TestProtectedWrites_ApprovalIsNotRemembered(t *testing.T) {
	f := newProtectedFixture(t)
	g := f.gate(settings.ModeAcceptEdits, settings.Permissions{}, nil)
	p := &promptRecorder{kind: PromptAllowAlways}
	g.SetPrompter(p.prompt)
	req := editReq(filepath.Join(f.root, ".git", "config"))
	for i := 0; i < 2; i++ {
		blocked, out, err := g.CheckWithOutcome(context.Background(), req)
		if err != nil || blocked != nil || out != OutcomeApproved {
			t.Fatalf("call %d: blocked=%+v out=%q err=%v, want approved at the prompt", i, blocked, out, err)
		}
	}
	if len(p.reqs) != 2 {
		t.Errorf("prompts = %d, want 2: a protected write must ask every time", len(p.reqs))
	}
	if grants := g.SessionGrants(); len(grants) != 0 {
		t.Errorf("session grants = %v, want none", grants)
	}
}

// Print mode has nobody to ask: the write is refused with a reason that
// names the protected path and tells the model not to route around it.
func TestProtectedWrites_PrintModeRefuses(t *testing.T) {
	f := newProtectedFixture(t)
	for name, path := range f.protectedTargets() {
		for _, mode := range []settings.PermissionMode{settings.ModeManual, settings.ModeAcceptEdits} {
			g := f.gate(mode, settings.Permissions{Allow: []string{"Edit"}}, nil)
			blocked, _, err := g.CheckWithOutcome(context.Background(), editReq(path))
			if err != nil {
				t.Fatal(err)
			}
			if blocked == nil {
				t.Errorf("%s in %s print mode: allowed, want refused", name, mode)
				continue
			}
			for _, want := range []string{"protected path", "user's approval", "Do not try to change it another way"} {
				if !strings.Contains(blocked.Reason, want) {
					t.Errorf("%s in %s: reason %q lacks %q", name, mode, blocked.Reason, want)
				}
			}
		}
	}
}

// dontAsk refuses what would ask, an allow rule notwithstanding.
func TestProtectedWrites_DontAskRefuses(t *testing.T) {
	f := newProtectedFixture(t)
	for name, path := range f.protectedTargets() {
		g := f.gate(settings.ModeDontAsk, settings.Permissions{Allow: []string{"Edit", "Write"}}, nil)
		p := &promptRecorder{kind: PromptAllow}
		g.SetPrompter(p.prompt)
		blocked, _, err := g.CheckWithOutcome(context.Background(), editReq(path))
		if err != nil {
			t.Fatal(err)
		}
		if blocked == nil || len(p.reqs) != 0 || !strings.Contains(blocked.Reason, "protected path") {
			t.Errorf("%s in dontAsk: blocked=%+v prompts=%d, want a protected-path refusal without a prompt", name, blocked, len(p.reqs))
		}
	}
}

// Plan mode refuses edits outright, protected or not; the refusal stays a
// refusal rather than becoming a prompt.
func TestProtectedWrites_PlanModeStillRefusesEdits(t *testing.T) {
	f := newProtectedFixture(t)
	for name, path := range f.protectedTargets() {
		g := f.gate(settings.ModePlan, settings.Permissions{}, nil)
		p := &promptRecorder{kind: PromptAllow}
		g.SetPrompter(p.prompt)
		blocked, _, err := g.CheckWithOutcome(context.Background(), editReq(path))
		if err != nil {
			t.Fatal(err)
		}
		if blocked == nil || len(p.reqs) != 0 {
			t.Errorf("%s in plan: blocked=%v prompts=%d, want plan mode's refusal", name, blocked != nil, len(p.reqs))
		}
	}
}

// bypassPermissions allows protected-path writes: Claude Code's
// permission-modes docs list them as "Allowed" in that mode. Deny rules
// still hold.
func TestProtectedWrites_BypassAllowsButDenyHolds(t *testing.T) {
	f := newProtectedFixture(t)
	for name, path := range f.protectedTargets() {
		g := f.gate(settings.ModeBypassPermissions, settings.Permissions{}, nil)
		blocked, out, err := g.CheckWithOutcome(context.Background(), editReq(path))
		if err != nil || blocked != nil || out != OutcomeAuto {
			t.Errorf("%s in bypass: blocked=%+v out=%q err=%v, want allowed", name, blocked, out, err)
		}
	}
	for _, mode := range []settings.PermissionMode{settings.ModeBypassPermissions, settings.ModeAcceptEdits, settings.ModeManual} {
		g := f.gate(mode, settings.Permissions{Deny: []string{"Edit(/.git/**)"}}, nil)
		p := &promptRecorder{kind: PromptAllow}
		g.SetPrompter(p.prompt)
		blocked, _, err := g.CheckWithOutcome(context.Background(), editReq(filepath.Join(f.root, ".git", "config")))
		if err != nil {
			t.Fatal(err)
		}
		if blocked == nil || len(p.reqs) != 0 || !strings.Contains(blocked.Reason, "permission rules") {
			t.Errorf("deny rule in %s: blocked=%+v prompts=%d, want the deny rule's refusal", mode, blocked, len(p.reqs))
		}
	}
}

// Auto mode routes a protected write to the classifier, past an allow
// rule; a write that only resolves to a protected path through a symlink
// asks instead, since the classifier would judge the harmless name.
func TestProtectedWrites_AutoMode(t *testing.T) {
	f := newProtectedFixture(t)
	for name, path := range f.protectedTargets() {
		c := allowAll()
		g := f.gate(settings.ModeAuto, settings.Permissions{Allow: []string{"Edit(/**)"}}, c)
		p := &promptRecorder{kind: PromptDeny}
		g.SetPrompter(p.prompt)
		if _, _, err := g.CheckWithOutcome(context.Background(), editReq(path)); err != nil {
			t.Fatal(err)
		}
		if name == "link to .git/config" {
			if len(c.calls) != 0 || len(p.reqs) != 1 {
				t.Errorf("%s in auto: classifier=%d prompts=%d, want a prompt and no classifier", name, len(c.calls), len(p.reqs))
			}
			continue
		}
		if len(c.calls) != 1 || len(p.reqs) != 0 {
			t.Errorf("%s in auto: classifier=%d prompts=%d, want the classifier only", name, len(c.calls), len(p.reqs))
		}
	}
}

// Ordinary files keep their mode's behaviour: acceptEdits writes them
// without asking, and so does manual mode under an allow rule. CLAUDE.md
// is protected in auto mode only (protected.go, autoModeOnlyFiles).
func TestProtectedWrites_OrdinaryFilesUnchanged(t *testing.T) {
	f := newProtectedFixture(t)
	ordinary := []string{
		filepath.Join(f.root, "src", "main.go"),
		filepath.Join(f.root, "docs", "git.md"),
		filepath.Join(f.root, ".claude", "worktrees", "w1", "a.go"),
		filepath.Join(f.root, "CLAUDE.md"),
		filepath.Join(f.root, ".gitignore"),
		"notes.txt",
	}
	for _, path := range ordinary {
		for _, tc := range []struct {
			mode  settings.PermissionMode
			allow []string
		}{{settings.ModeAcceptEdits, nil}, {settings.ModeManual, []string{"Edit"}}, {settings.ModeDontAsk, []string{"Edit"}}} {
			g := f.gate(tc.mode, settings.Permissions{Allow: tc.allow}, nil)
			p := &promptRecorder{kind: PromptDeny}
			g.SetPrompter(p.prompt)
			blocked, out, err := g.CheckWithOutcome(context.Background(), editReq(path))
			if err != nil || blocked != nil || out != OutcomeAuto || len(p.reqs) != 0 {
				t.Errorf("%s in %s: blocked=%+v out=%q prompts=%d, want auto-approved", path, tc.mode, blocked, out, len(p.reqs))
			}
		}
	}
}

// Bash writes kiln's analysis can name are held to the same check outside
// auto mode, as Claude Code checks the paths a command writes before its
// allow rules: a redirect or a cp into a protected path asks even under a
// rule that allows the command. Writes it cannot name are not (Claude
// Code's other modes do not look further either).
func TestProtectedWrites_BashOutsideAutoMode(t *testing.T) {
	f := newProtectedFixture(t)
	asks := []struct {
		allow []string
		cmd   string
	}{
		{[]string{"Bash(echo *)"}, "echo '[core] hooksPath = x' > .git/config"},
		{[]string{"Bash(echo *)"}, "echo x >> .GIT/Config"},
		{[]string{"Bash(echo *)"}, "echo x > settings.ini"},
		{[]string{"Bash(echo *)"}, "echo x > .claude/settings.json"},
		{[]string{"Bash(echo *)"}, "echo x >> ~/.zshrc"},
		{[]string{"Bash(cp *)"}, "cp a.sh .git/hooks/pre-commit"},
		{[]string{"Bash(tee *)"}, "tee .vscode/tasks.json"},
		{nil, "printf '{}' > .mcp.json"},
	}
	for _, tc := range asks {
		for _, mode := range []settings.PermissionMode{settings.ModeManual, settings.ModeAcceptEdits, settings.ModePlan} {
			g := f.gate(mode, settings.Permissions{Allow: tc.allow}, nil)
			p := &promptRecorder{kind: PromptDeny}
			g.SetPrompter(p.prompt)
			blocked, _, err := g.CheckWithOutcome(context.Background(), bashReq(tc.cmd))
			if err != nil {
				t.Fatal(err)
			}
			if len(p.reqs) != 1 || blocked == nil {
				t.Errorf("%q in %s (allow %v): prompts=%d blocked=%v, want one prompt", tc.cmd, mode, tc.allow, len(p.reqs), blocked != nil)
				continue
			}
			if p.reqs[0].Grantable || !strings.Contains(p.reqs[0].AutoModeNote, "protected path") {
				t.Errorf("%q in %s: grantable=%v note=%q", tc.cmd, mode, p.reqs[0].Grantable, p.reqs[0].AutoModeNote)
			}
		}
		g := f.gate(settings.ModeAcceptEdits, settings.Permissions{Allow: tc.allow}, nil)
		blocked, _, err := g.CheckWithOutcome(context.Background(), bashReq(tc.cmd))
		if err != nil || blocked == nil || !strings.Contains(blocked.Reason, "protected path") {
			t.Errorf("%q in print mode: blocked=%+v err=%v, want a protected-path refusal", tc.cmd, blocked, err)
		}
		g = f.gate(settings.ModeBypassPermissions, settings.Permissions{}, nil)
		if blocked, _, err := g.CheckWithOutcome(context.Background(), bashReq(tc.cmd)); err != nil || blocked != nil {
			t.Errorf("%q in bypass: blocked=%+v err=%v, want allowed", tc.cmd, blocked, err)
		}
	}
	// Unprotected writes, and protected paths only read, keep the allow
	// rule and the read-only fast path.
	for _, tc := range []struct {
		allow []string
		cmd   string
	}{
		{[]string{"Bash(echo *)"}, "echo hi > out.txt"},
		{nil, "cat .git/config"},
		{nil, "git log --oneline -3"},
	} {
		g := f.gate(settings.ModeAcceptEdits, settings.Permissions{Allow: tc.allow}, nil)
		p := &promptRecorder{kind: PromptDeny}
		g.SetPrompter(p.prompt)
		blocked, _, err := g.CheckWithOutcome(context.Background(), bashReq(tc.cmd))
		if err != nil || blocked != nil || len(p.reqs) != 0 {
			t.Errorf("%q: blocked=%+v prompts=%d, want allowed without a prompt", tc.cmd, blocked, len(p.reqs))
		}
	}
}

// In auto mode a bash write that only resolves to a protected path asks.
func TestProtectedWrites_AutoModeBashThroughLinkAsks(t *testing.T) {
	f := newProtectedFixture(t)
	c := allowAll()
	g := f.gate(settings.ModeAuto, settings.Permissions{Allow: []string{"Bash(echo *)"}}, c)
	p := &promptRecorder{kind: PromptDeny}
	g.SetPrompter(p.prompt)
	if _, _, err := g.CheckWithOutcome(context.Background(), bashReq("echo x > settings.ini")); err != nil {
		t.Fatal(err)
	}
	if len(c.calls) != 0 || len(p.reqs) != 1 {
		t.Errorf("classifier=%d prompts=%d, want a prompt and no classifier", len(c.calls), len(p.reqs))
	}
}
