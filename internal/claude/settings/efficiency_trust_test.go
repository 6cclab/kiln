package settings

import (
	"path/filepath"
	"testing"
)

func TestProjectCannotEnableOptInLearningOrUndoMemoryOptOut(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	user := filepath.Join(home, ".kiln", "settings.json")
	project := filepath.Join(cwd, ".claude", "settings.json")
	put(t, user, `{"learningEnabled":false,"personalMemoryEnabled":false,"memoryAutoRetrieve":false,"memoryRetrievalTokens":128,"learningAutomation":{"enabled":false,"maxOutcomes":10}}`)
	put(t, project, `{"learningEnabled":true,"personalMemoryEnabled":true,"memoryAutoRetrieve":true,"memoryRetrievalTokens":8192,"learningAutomation":{"enabled":true,"maxOutcomes":1000}}`)
	got := LoadSettings(cwd, LoadOptions{Headless: true, Trusted: false})
	if *got.LearningEnabled || *got.PersonalMemoryEnabled || *got.MemoryAutoRetrieve || *got.MemoryRetrievalTokens != 128 || *got.LearningAutomation.Enabled || *got.LearningAutomation.MaxOutcomes != 10 {
		t.Fatalf("project undid user privacy controls: %+v", got)
	}
	sources := got.EfficiencyProvenance()
	for _, key := range []string{"learningEnabled", "personalMemoryEnabled", "memoryAutoRetrieve", "memoryRetrievalTokens", "learningAutomation.enabled", "learningAutomation.maxOutcomes"} {
		if sources[key] != user {
			t.Fatalf("rejected %s override source = %q", key, sources[key])
		}
	}
	// The same project file cannot start opt-in capture when the user has
	// never set a learningAutomation.enabled value at all.
	put(t, user, `{}`)
	got = LoadSettings(cwd, LoadOptions{Headless: true, Trusted: false})
	if got.LearningAutomation.Enabled != nil {
		t.Fatalf("project enabled opt-in capture from default: %+v", got.LearningAutomation)
	}
}

func TestProjectCanDisableAndExplicitSettingsCanReenableMemoryLearning(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	put(t, filepath.Join(home, ".kiln", "settings.json"), `{"learningEnabled":true,"personalMemoryEnabled":true,"memoryAutoRetrieve":true,"memoryRetrievalTokens":512,"learningAutomation":{"enabled":true,"maxOutcomes":100}}`)
	project := filepath.Join(cwd, ".claude", "settings.json")
	put(t, project, `{"learningEnabled":false,"personalMemoryEnabled":false,"memoryAutoRetrieve":false,"memoryRetrievalTokens":256,"learningAutomation":{"enabled":false,"maxOutcomes":50}}`)
	got := LoadSettings(cwd, LoadOptions{Headless: true, Trusted: false})
	if *got.LearningEnabled || *got.PersonalMemoryEnabled || *got.MemoryAutoRetrieve || *got.MemoryRetrievalTokens != 256 || *got.LearningAutomation.Enabled || *got.LearningAutomation.MaxOutcomes != 50 {
		t.Fatalf("project tightening ignored: %+v", got)
	}
	if got.EfficiencyProvenance()["learningAutomation.enabled"] != project {
		t.Fatal("project disable provenance missing")
	}
	extra := filepath.Join(t.TempDir(), "explicit-settings.json")
	put(t, extra, `{"learningEnabled":true,"personalMemoryEnabled":true,"memoryAutoRetrieve":true,"memoryRetrievalTokens":1024,"learningAutomation":{"enabled":true,"maxOutcomes":200}}`)
	got = LoadSettings(cwd, LoadOptions{Headless: true, Trusted: false, Extra: extra})
	if !*got.LearningEnabled || !*got.PersonalMemoryEnabled || !*got.MemoryAutoRetrieve || *got.MemoryRetrievalTokens != 1024 || !*got.LearningAutomation.Enabled || *got.LearningAutomation.MaxOutcomes != 200 {
		t.Fatalf("explicit settings failed to reenable: %+v", got)
	}
	if got.EfficiencyProvenance()["learningAutomation.enabled"] != extra {
		t.Fatal("explicit reenable provenance missing")
	}
}
