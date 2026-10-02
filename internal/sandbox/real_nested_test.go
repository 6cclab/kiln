//go:build darwin || linux

package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A nested project inside the workspace is protected like the root: its
// .claude settings, hooks, skills, commands and agents, .mcp.json, .kiln
// and .git hooks/config (kiln's trust covers a folder's subdirectories).
// macOS holds them by pattern, so new ones too; Linux binds the paths
// that exist.
func TestRealSandboxNestedProjectConfig(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Linux holds only what exists in the roots and above them")
	}
	r := newRealRig(t, Config{}, nil)
	os.MkdirAll(filepath.Join(r.ws, "sub", ".git", "hooks"), 0o755)
	for _, rel := range []string{
		"sub/.claude/settings.json", "sub/.claude/settings.local.json", "sub/.claude/hooks/x.sh",
		"sub/.claude/skills/s/SKILL.md", "sub/.claude/commands/c.md", "sub/.claude/agents/a.md",
		"sub/.mcp.json", "sub/deeper/.kiln/settings.local.json", "sub/.git/hooks/pre-commit", "sub/.git/config",
	} {
		r.run("mkdir -p " + filepath.Dir(rel) + " && echo x > " + rel)
		mustNotExist(t, filepath.Join(r.ws, rel))
	}
	// Other files in a nested .claude stay writable.
	if out, code := r.run("mkdir -p sub/.claude/notes && echo x > sub/.claude/notes/n.md"); code != 0 {
		t.Errorf("ordinary file in a nested .claude refused: %q", out)
	}
}

// The home directory's shell startup files and login-item folders are
// protected even when the home directory itself is a workspace root.
func TestRealSandboxHomeAsRoot(t *testing.T) {
	r := newRealRig(t, Config{}, nil)
	r.m.opts.Roots = func() []string { return []string{r.ws, r.home} }
	for _, rel := range []string{".zshrc", ".bash_profile", ".profile", "Library/LaunchAgents/evil.plist", ".config/autostart/evil.desktop"} {
		p := filepath.Join(r.home, rel)
		r.run("mkdir -p " + filepath.Dir(p) + "; echo x > " + p)
		mustNotExist(t, p)
	}
	if out, code := r.run("echo ok > " + filepath.Join(r.home, "notes.txt")); code != 0 {
		t.Errorf("an ordinary file in a home root refused: %q", out)
	}
}
