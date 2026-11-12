package settings

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestEfficiencyProvenanceTracksAcceptedOverrides(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	user := filepath.Join(home, ".kiln", "settings.json")
	local := filepath.Join(cwd, ".kiln", "settings.local.json")
	put(t, user, `{"taskVerificationStatus":true,"taskStateEnabled":true,"taskStateTokens":256,"requestBudget":{"maxCalls":5,"freeModels":["test"]},"runtimeEfficiency":{"readCache":true,"readCacheEntries":16,"readCacheBytes":4096,"progressLimit":3},"skillDiscovery":{"unified":true,"includeAgents":true}}`)
	put(t, local, `{"taskVerificationStatus":false,"taskStateEnabled":false,"taskStateTokens":256,"requestBudget":{"maxCalls":0,"freeModels":[]},"runtimeEfficiency":{"readCache":false,"readCacheEntries":0,"readCacheBytes":67108865,"progressLimit":0},"skillDiscovery":{"unified":false,"includeAgents":false}}`)
	got := LoadSettings(cwd, LoadOptions{Trusted: true})
	sources := got.EfficiencyProvenance()
	for _, k := range []string{"taskVerificationStatus", "skillDiscovery.includeAgents", "taskStateEnabled", "taskStateTokens", "requestBudget.freeModels", "runtimeEfficiency.readCache", "runtimeEfficiency.progressLimit", "skillDiscovery.unified"} {
		if sources[k] != local {
			t.Errorf("%s source = %q", k, sources[k])
		}
	}
	for _, k := range []string{"requestBudget.maxCalls", "runtimeEfficiency.readCacheEntries", "runtimeEfficiency.readCacheBytes"} {
		if sources[k] != user {
			t.Errorf("%s source = %q", k, sources[k])
		}
	}
	if sources["memoryRetrievalTokens"] != "default" {
		t.Fatal(sources)
	}
	if *got.TaskVerificationStatus || *got.SkillDiscovery.IncludeAgents || *got.RuntimeEfficiency.ReadCache || *got.RuntimeEfficiency.ReadCacheEntries != 16 || *got.RuntimeEfficiency.ReadCacheBytes != 4096 || *got.RuntimeEfficiency.ProgressLimit != 0 || *got.SkillDiscovery.Unified || *got.RequestBudget.MaxCalls != 5 || len(got.RequestBudget.FreeModels) != 0 {
		t.Fatalf("unexpected effective config: %+v", got)
	}
}

func TestRuntimeEfficiencyRejectsInvalidProgressLimits(t *testing.T) {
	for _, n := range []int{-1, 1, 21} {
		previous := 4
		r := RuntimeEfficiency{ProgressLimit: &previous}
		r.merge(RuntimeEfficiency{ProgressLimit: &n})
		if *r.ProgressLimit != 4 {
			t.Fatalf("accepted %d", n)
		}
	}
}

func TestRouterConservativeSourceReplacement(t *testing.T) {
	var r ModelRouterConfig
	r.applyRouter("user", json.RawMessage(`{"conservative":false}`))
	if r.ConservativeSource != "user" || !r.ConservativeSet {
		t.Fatalf("%+v", r)
	}
	for _, raw := range []string{`true`, `false`, `{}`} {
		r.applyRouter("user", json.RawMessage(`{"conservative":true}`))
		r.applyRouter("extra", json.RawMessage(raw))
		if r.ConservativeSource != "" || r.ConservativeSet {
			t.Fatalf("%s: %+v", raw, r)
		}
	}
}
