package settings

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestProjectCannotRelaxUserRequestBudget(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	user := filepath.Join(home, ".kiln", "settings.json")
	project := filepath.Join(cwd, ".claude", "settings.json")
	put(t, user, `{"requestBudget":{"maxCalls":3,"maxTokens":10000,"maxUSD":1.5,"freeModels":["local/known"]}}`)
	put(t, project, `{"requestBudget":{"maxCalls":0,"maxTokens":20000,"maxUSD":0,"freeModels":["local/known","paid/unknown"]}}`)
	got := LoadSettings(cwd, LoadOptions{Headless: true, Trusted: false})
	if got.RequestBudget.MaxCalls == nil || *got.RequestBudget.MaxCalls != 3 || got.RequestBudget.MaxTokens == nil || *got.RequestBudget.MaxTokens != 10000 || got.RequestBudget.MaxUSD == nil || *got.RequestBudget.MaxUSD != 1.5 {
		t.Fatalf("project relaxed inherited limits: %+v", got.RequestBudget)
	}
	if !reflect.DeepEqual(got.RequestBudget.FreeModels, []string{"local/known"}) {
		t.Fatalf("project added free model exemption: %v", got.RequestBudget.FreeModels)
	}
	sources := got.EfficiencyProvenance()
	for _, key := range []string{"requestBudget.maxCalls", "requestBudget.maxTokens", "requestBudget.maxUSD", "requestBudget.freeModels"} {
		if sources[key] != user {
			t.Fatalf("rejected %s override source = %q", key, sources[key])
		}
	}
}

func TestProjectCanTightenAndExplicitSettingsCanRelaxBudget(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	put(t, filepath.Join(home, ".kiln", "settings.json"), `{"requestBudget":{"maxCalls":8,"maxTokens":20000,"maxUSD":4,"freeModels":["local/a","local/b"]}}`)
	project := filepath.Join(cwd, ".claude", "settings.json")
	put(t, project, `{"requestBudget":{"maxCalls":4,"maxTokens":12000,"maxUSD":2,"freeModels":["local/a","paid/new"]}}`)
	got := LoadSettings(cwd, LoadOptions{Headless: true, Trusted: false})
	if *got.RequestBudget.MaxCalls != 4 || *got.RequestBudget.MaxTokens != 12000 || *got.RequestBudget.MaxUSD != 2 || !reflect.DeepEqual(got.RequestBudget.FreeModels, []string{"local/a"}) {
		t.Fatalf("project tightening lost: %+v", got.RequestBudget)
	}
	if got.EfficiencyProvenance()["requestBudget.freeModels"] != project {
		t.Fatal("free model removal did not get project provenance")
	}
	extra := filepath.Join(t.TempDir(), "explicit-settings.json")
	put(t, extra, `{"requestBudget":{"maxCalls":0,"maxTokens":0,"maxUSD":0,"freeModels":["paid/new"]}}`)
	got = LoadSettings(cwd, LoadOptions{Headless: true, Trusted: false, Extra: extra})
	if *got.RequestBudget.MaxCalls != 0 || *got.RequestBudget.MaxTokens != 0 || *got.RequestBudget.MaxUSD != 0 || !reflect.DeepEqual(got.RequestBudget.FreeModels, []string{"paid/new"}) {
		t.Fatalf("explicit settings did not override: %+v", got.RequestBudget)
	}
}
