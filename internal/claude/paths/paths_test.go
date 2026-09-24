package paths

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := "/some/project"

	roots := ClaudeRoots(cwd)
	if len(roots) != 2 {
		t.Fatalf("expected 2 roots, got %d", len(roots))
	}
	if roots[0].Scope != ScopeUser || roots[0].Dir != filepath.Join(home, ".claude") {
		t.Errorf("user root = %+v", roots[0])
	}
	if roots[1].Scope != ScopeProject || roots[1].Dir != filepath.Join(cwd, ".claude") {
		t.Errorf("project root = %+v", roots[1])
	}
}

func TestSettingsFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := "/some/project"

	files := SettingsFiles(cwd)
	if len(files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(files))
	}
	want := []struct {
		scope Scope
		path  string
	}{
		{ScopeUser, filepath.Join(home, ".claude", "settings.json")},
		{ScopeProject, filepath.Join(cwd, ".claude", "settings.json")},
		{ScopeLocal, filepath.Join(cwd, ".claude", "settings.local.json")},
	}
	for i, w := range want {
		if files[i].Scope != w.scope || files[i].Path != w.path {
			t.Errorf("files[%d] = %+v, want %+v", i, files[i], w)
		}
	}
}

func TestKeybindingsPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := KeybindingsPath(); got != filepath.Join(home, ".claude", "keybindings.json") {
		t.Errorf("got %q", got)
	}
	if !strings.HasSuffix(KeybindingsPath(), "/.claude/keybindings.json") {
		t.Errorf("got %q", KeybindingsPath())
	}
}

func TestClaudeJSONPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := ClaudeJSONPath(); got != filepath.Join(home, ".claude.json") {
		t.Errorf("got %q", got)
	}
}

func TestCLAUDEMDConst(t *testing.T) {
	if CLAUDEMD != "CLAUDE.md" {
		t.Errorf("got %q", CLAUDEMD)
	}
}
