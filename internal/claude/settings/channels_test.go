package settings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/andrepato/harness/internal/claude/paths"
)

func writeChannelSettings(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The headline case for kiln's settings-driven channels: a channel turned on
// globally in the user scope is active everywhere, and a single project can
// override it off in its own settings — the per-key merge that the flag-based
// Claude Code design can't express.
func TestLoadChannelsGlobalWithProjectOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	writeChannelSettings(t, filepath.Join(home, ".claude", "settings.json"),
		`{"channels": {"design-studio@6cclab": true, "alerts@ops": true}}`)
	writeChannelSettings(t, filepath.Join(cwd, ".claude", "settings.json"),
		`{"channels": {"design-studio@6cclab": false}}`)
	writeChannelSettings(t, filepath.Join(cwd, ".claude", "settings.local.json"),
		`{"channels": {"logs@ops": true}}`)

	got := LoadChannels(cwd)
	want := map[string]bool{
		"design-studio@6cclab": false, // user on, project overrode off
		"alerts@ops":           true,  // inherited from user, untouched
		"logs@ops":             true,  // added by local
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("channel %q = %v, want %v", k, got[k], v)
		}
	}
}

// --setting-sources user: a channel only the project enables must stay off,
// matching LoadEnabledPluginsFrom.
func TestLoadChannelsFromRestrictsScopes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	writeChannelSettings(t, filepath.Join(home, ".claude", "settings.json"),
		`{"channels": {"design-studio@6cclab": true}}`)
	writeChannelSettings(t, filepath.Join(cwd, ".claude", "settings.json"),
		`{"channels": {"project-only@mp": true}}`)

	got := LoadChannelsFrom(cwd, []paths.Scope{paths.ScopeUser})
	if !got["design-studio@6cclab"] {
		t.Errorf("user-scope channel should be on, got %v", got)
	}
	if _, present := got["project-only@mp"]; present {
		t.Errorf("project-scope channel should be absent under --setting-sources user, got %v", got)
	}
}

// Absent "channels" across every scope is an empty map, never a panic.
func TestLoadChannelsAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	writeChannelSettings(t, filepath.Join(cwd, ".claude", "settings.json"), `{"model": "anthropic/claude-sonnet-4-5"}`)

	if got := LoadChannels(cwd); len(got) != 0 {
		t.Errorf("LoadChannels = %v, want empty", got)
	}
}
