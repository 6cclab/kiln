package settings

import (
	"path/filepath"
	"testing"
)

// TestLearningReviewOnlyUserSettingsEnableIt: the automatic review costs a
// model call, so a repository cannot turn it on or make it run more often;
// it can turn it off or make it run less often. User settings decide.
func TestLearningReviewOnlyUserSettingsEnableIt(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	user := filepath.Join(home, ".kiln", "settings.json")
	project := filepath.Join(cwd, ".claude", "settings.json")

	put(t, user, `{}`)
	put(t, project, `{"learningReview":{"enabled":true,"everyPrompts":1,"everyToolCalls":1}}`)
	got := LoadSettings(cwd, LoadOptions{Headless: true, Trusted: true}).LearningReview
	if got.Enabled != nil || got.EveryPrompts != nil || got.EveryToolCalls != nil {
		t.Fatalf("project enabled the review or raised its frequency: %+v", got)
	}

	put(t, user, `{"learningReview":{"enabled":true,"everyPrompts":5}}`)
	put(t, project, `{"learningReview":{"everyPrompts":3,"everyToolCalls":40}}`)
	got = LoadSettings(cwd, LoadOptions{Headless: true, Trusted: true}).LearningReview
	if got.Enabled == nil || !*got.Enabled || *got.EveryPrompts != 5 || *got.EveryToolCalls != 40 {
		t.Fatalf("got %+v, want enabled by the user, every 5 prompts (project's 3 refused), every 40 tool calls (project's slower cadence kept)", got)
	}

	put(t, project, `{"learningReview":{"enabled":false}}`)
	if got = LoadSettings(cwd, LoadOptions{Headless: true, Trusted: true}).LearningReview; *got.Enabled {
		t.Fatalf("project could not turn the review off: %+v", got)
	}
}
