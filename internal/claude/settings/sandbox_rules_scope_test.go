package settings

import (
	"path/filepath"
	"testing"
)

// Permission rules from a repository's settings do not open the sandbox
// wide, even once the folder is trusted: Edit(~/**) and WebFetch(domain:*)
// there add nothing to it (and are named in a warning); from user
// settings they do.
func TestSandboxRulesFromRepositoryDoNotWiden(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	put(t, filepath.Join(cwd, ".claude", "settings.json"), `{"permissions":{"allow":["Edit(~/**)","Edit(//**)","Edit(./build/**)","WebFetch(domain:*)","WebFetch(domain:go.dev)"]}}`)
	s := LoadSettings(cwd, LoadOptions{Trusted: true})
	aw, _, _ := SandboxRulePaths(s.Permissions, cwd)
	if len(aw) != 1 || aw[0].Base != filepath.Join(cwd, "build") {
		t.Errorf("allowWrite from repository rules = %+v", aw)
	}
	allow, _ := SandboxRuleDomains(s.Permissions)
	if len(allow) != 1 || allow[0] != "go.dev" {
		t.Errorf("domains from repository rules = %v", allow)
	}
	if w := SandboxRuleWarnings(s.Permissions, cwd); len(w) != 3 {
		t.Errorf("warnings = %v", w)
	}

	put(t, filepath.Join(home, ".claude", "settings.json"), `{"permissions":{"allow":["Edit(~/**)","WebFetch(domain:*)"]}}`)
	s = LoadSettings(cwd, LoadOptions{Trusted: true})
	aw, _, _ = SandboxRulePaths(s.Permissions, cwd)
	allow, _ = SandboxRuleDomains(s.Permissions)
	if len(aw) != 2 || len(allow) != 2 {
		t.Errorf("user rules should widen: %+v %v", aw, allow)
	}
}
