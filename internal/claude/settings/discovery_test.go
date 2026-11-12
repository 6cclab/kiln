package settings

import (
	"path/filepath"
	"testing"
)

func TestDiscoverySettingsPrecedence(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	put(t, filepath.Join(home, ".kiln", "settings.json"), `{"skillDiscovery":{"mode":"search","budgetTokens":512,"pinned":["sql"]}}`)
	put(t, filepath.Join(cwd, ".kiln", "settings.local.json"), `{"skillDiscovery":{"mode":"auto","pinned":[]}}`)
	got := LoadSettings(cwd, LoadOptions{Trusted: true}).SkillDiscovery
	if got.Mode != "auto" || got.Budget(2048) != 512 || len(got.Pinned) != 0 {
		t.Fatalf("%+v", got)
	}
	if got.Sources["mode"] != filepath.Join(cwd, ".kiln", "settings.local.json") {
		t.Fatal(got.Sources)
	}
	if (SkillDiscovery{}).Budget(3000) != 1024 || got.Budget(256) != 256 {
		t.Fatal("budget cap")
	}
}

func TestMemoryAndDiscoveryPermissions(t *testing.T) {
	if !ReadOnly["skill_search"] || !ReadOnly["memory_recall"] || ReadOnly["memory_write"] {
		t.Fatal("incorrect read-only classification")
	}
	if !PlanOverridesAllow("memory_write", "remember") {
		t.Fatal("memory writes allowed in plan mode")
	}
}

func TestTaskStateSettingsMergeAndValidation(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	put(t, filepath.Join(home, ".kiln", "settings.json"), `{"taskStateEnabled":true,"taskStateTokens":256}`)
	put(t, filepath.Join(cwd, ".kiln", "settings.local.json"), `{"taskStateEnabled":false,"taskStateTokens":-1}`)
	got := LoadSettings(cwd, LoadOptions{Trusted: true})
	if got.TaskStateEnabled == nil || *got.TaskStateEnabled || PositiveOr(got.TaskStateTokens, 512) != 256 {
		t.Fatalf("task state: %+v", got)
	}
}
