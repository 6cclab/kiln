package settings

import (
	"path/filepath"
	"testing"
)

func TestLearningAutomationConfigurationAndProvenance(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	user := filepath.Join(home, ".kiln", "settings.json")
	local := filepath.Join(cwd, ".kiln", "settings.local.json")
	put(t, user, `{"learningAutomation":{"enabled":true,"maxOutcomes":10,"minSamples":3},"runtimeEfficiency":{"cycleLimit":3}}`)
	put(t, local, `{"learningAutomation":{"enabled":false,"maxOutcomes":2,"minSamples":5},"runtimeEfficiency":{"cycleLimit":0}}`)
	got := LoadSettings(cwd, LoadOptions{Trusted: true})
	if *got.LearningAutomation.Enabled || *got.LearningAutomation.MaxOutcomes != 10 || *got.LearningAutomation.MinSamples != 3 || *got.RuntimeEfficiency.CycleLimit != 0 {
		t.Fatalf("unexpected merged controls: %+v %+v", got.LearningAutomation, got.RuntimeEfficiency)
	}
	sources := got.EfficiencyProvenance()
	for _, key := range []string{"learningAutomation.enabled", "runtimeEfficiency.cycleLimit"} {
		if sources[key] != local {
			t.Fatalf("%s: %s", key, sources[key])
		}
	}
	for _, key := range []string{"learningAutomation.maxOutcomes", "learningAutomation.minSamples"} {
		if sources[key] != user {
			t.Fatalf("%s: %s", key, sources[key])
		}
	}
}

func TestLearningAutomationRejectsInvalidBounds(t *testing.T) {
	for _, tc := range []struct{ max, samples int }{{0, 1}, {1001, 101}, {-1, -1}} {
		var cfg LearningAutomation
		cfg.merge(LearningAutomation{MaxOutcomes: &tc.max, MinSamples: &tc.samples})
		if cfg.MaxOutcomes != nil || cfg.MinSamples != nil {
			t.Fatalf("accepted invalid bounds: %+v", cfg)
		}
	}
	for _, n := range []int{-1, 1, 11} {
		v := 3
		cfg := RuntimeEfficiency{CycleLimit: &v}
		cfg.merge(RuntimeEfficiency{CycleLimit: &n})
		if *cfg.CycleLimit != 3 {
			t.Fatalf("accepted cycle limit %d", n)
		}
	}
}
