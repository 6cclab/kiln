package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestContextSettingEntries_PlainWords: /context names the permission mode the
// way the footer does and says where each setting came from in plain words,
// not as raw flag, variable or provenance keys (Codex polish finding:
// "permission manual · default" beside a footer reading "ask before edits").
func TestContextSettingEntries_PlainWords(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	r := contextRuntimeSettings{
		Model: "faux/faux-1", Permission: "manual", AutoMemoryDirectory: "/x",
		Sources: map[string]string{
			"model":                   "--model",
			"effortLevel":             "default",
			"permissions.defaultMode": filepath.Join(home, ".claude", "settings.json"),
			"autoMemoryDirectory":     "HARNESS_EXP_PLAN_EFFORT",
		},
	}
	want := []string{
		"model: faux/faux-1 · set by --model",
		"effort: model default",
		"permission: ask before edits · from ~/.claude/settings.json",
		"auto-memory dir: /x · set by HARNESS_EXP_PLAN_EFFORT",
	}
	got := r.entries()
	if len(got) != len(want) {
		t.Fatalf("entries = %+v", got)
	}
	for i := range want {
		if line := got[i].Line(); line != want[i] {
			t.Errorf("row %d = %q, want %q", i, line, want[i])
		}
	}
	if got := r.effectiveForLane("", "", "plan").entries()[2].Line(); got != "permission: plan only · changed this session" {
		t.Errorf("session change = %q", got)
	}
}
