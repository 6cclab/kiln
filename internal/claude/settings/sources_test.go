package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProvenanceTracksAcceptedNonEfficiencyOverrides(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	user := filepath.Join(home, ".claude", "settings.json")
	project := filepath.Join(cwd, ".claude", "settings.json")
	for path, contents := range map[string]string{
		user:    `{"model":"user/model","modelRoles":{"fast":"user/fast"},"autoMemoryEnabled":true,"permissions":{"defaultMode":"acceptEdits"}}`,
		project: `{"model":"project/model","modelRoles":{"fast":"project/fast","slow":"project/slow"},"autoMemoryEnabled":false,"permissions":{"defaultMode":"auto"},"promptSuggestionEnabled":true}`,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s := LoadSettings(cwd, LoadOptions{Trusted: true})
	p := s.Provenance()
	for key, want := range map[string]string{
		"model":                   project,
		"modelRoles.fast":         project,
		"modelRoles.slow":         project,
		"autoMemoryEnabled":       project,
		"promptSuggestionEnabled": project,
		"permissions.defaultMode": "default",
		"effortLevel":             "default",
	} {
		if got := p[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}
