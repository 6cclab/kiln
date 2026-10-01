package paths

import (
	"path/filepath"
	"testing"
)

// TestAllSettingsFiles: kiln's two files sit right after Claude Code's
// file of the same scope, under .kiln.
func TestAllSettingsFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := "/proj"
	want := []SettingsSource{
		{SettingsFile: SettingsFile{ScopeUser, filepath.Join(home, ".claude", "settings.json")}},
		{SettingsFile: SettingsFile{ScopeUser, filepath.Join(home, ".kiln", "settings.json")}, Kiln: true},
		{SettingsFile: SettingsFile{ScopeProject, filepath.Join(cwd, ".claude", "settings.json")}},
		{SettingsFile: SettingsFile{ScopeLocal, filepath.Join(cwd, ".claude", "settings.local.json")}},
		{SettingsFile: SettingsFile{ScopeLocal, filepath.Join(cwd, ".kiln", "settings.local.json")}, Kiln: true},
	}
	got := AllSettingsFiles(cwd)
	if len(got) != len(want) {
		t.Fatalf("got %d files, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("file %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if got := KilnUserSettingsPath(); got != filepath.Join(home, ".kiln", "settings.json") {
		t.Errorf("KilnUserSettingsPath = %q", got)
	}
	if got := KilnLocalSettingsPath(cwd); got != filepath.Join(cwd, ".kiln", "settings.local.json") {
		t.Errorf("KilnLocalSettingsPath = %q", got)
	}
	if got := KilnUserMemoryPath(); got != filepath.Join(home, ".kiln", "CLAUDE.md") {
		t.Errorf("KilnUserMemoryPath = %q", got)
	}
}
