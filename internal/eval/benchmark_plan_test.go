package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
)

func fakeVersionBinary(t *testing.T, version string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-bin")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' '"+version+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func benchmarkTestPlan(t *testing.T) BenchmarkPlan {
	t.Helper()
	plan, err := BuildBenchmarkPlan(BenchmarkPlanOptions{
		KilnBin: fakeVersionBinary(t, "kiln-test 1"), ClaudeBin: fakeVersionBinary(t, "2.1.319 (Claude Code)"),
		ScenariosDir: filepath.Join("..", "..", "eval", "scenarios"), FixturesDir: filepath.Join("..", "..", "eval", "fixtures"),
		KilnModel: "anthropic/claude-opus-4-8", ClaudeModel: "claude-opus-4-8", Repeats: 2, MaxTotalUSD: 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestBuildBenchmarkPlanOfflineProvenanceAndBudget(t *testing.T) {
	plan := benchmarkTestPlan(t)
	if len(plan.Scenarios) != 4 || plan.TotalPairs != 8 || plan.TotalRuns != 16 || plan.ClaudePerRunCapUSD != 1.5625 || plan.KilnBinaryHash == "" || plan.ClaudeBinaryHash == "" || plan.ClaudeSetupHash == "" || plan.ClaudeVersion != "2.1.319 (Claude Code)" || !filepath.IsAbs(plan.ScenariosDir) || !strings.Contains(plan.BudgetLimitation, "not automatically enforced") {
		t.Fatalf("incomplete benchmark plan: %+v", plan)
	}
	for _, sc := range plan.Scenarios {
		if sc.DatasetHash == "" || sc.FixtureHash == "" || len(sc.Checks) == 0 {
			t.Fatalf("scenario not pinned: %+v", sc)
		}
	}
}

func TestValidateBenchmarkResultsExactCohort(t *testing.T) {
	plan := benchmarkTestPlan(t)
	var kiln []Record
	var external []ExternalRecord
	for _, sc := range plan.Scenarios {
		for repeat := 1; repeat <= plan.Repeats; repeat++ {
			checks := make([]CheckResult, 0, len(sc.Checks))
			for _, c := range sc.Checks {
				checks = append(checks, CheckResult{Name: c.Name, Type: c.Type, Pass: true})
			}
			k := Record{RunID: "batch", KilnVersion: plan.KilnVersion, BinaryHash: plan.KilnBinaryHash, Scenario: sc.Name, Repeat: repeat, DatasetHash: sc.DatasetHash, FixtureHash: sc.FixtureHash, Provider: "anthropic", Model: "claude-opus-4-8", OK: true, DurationMS: 1000, Usage: msg.Usage{Input: 100, Output: 10}, Checks: checks, CostUSD: .01}
			e := ExternalRecord{SchemaVersion: 1, Harness: "claude-code", HarnessVersion: plan.ClaudeVersion, Model: plan.ClaudeModel, ModelVersion: "claude-opus-4-8", SetupHash: plan.ClaudeSetupHash, Scenario: sc.Name, Repeat: repeat, DatasetHash: sc.DatasetHash, OK: boolPtr(true), DurationMS: 1100, Checks: checks, Usage: ExternalUsage{Direct: TokenUsage{Input: 100, Output: 10}}}
			kiln = append(kiln, k)
			external = append(external, e)
		}
	}
	if comparison, err := ValidateBenchmarkResults(plan, kiln, external); err != nil || len(comparison.Pairs) != 8 {
		t.Fatalf("valid cohort rejected: %+v %v", comparison, err)
	}
	for _, tt := range []struct {
		name   string
		mutate func([]Record, []ExternalRecord)
	}{
		{"kiln fixture", func(k []Record, _ []ExternalRecord) { k[0].FixtureHash = "other" }},
		{"external setup", func(_ []Record, e []ExternalRecord) { e[0].SetupHash = "other" }},
		{"kiln run ID", func(k []Record, _ []ExternalRecord) { k[0].RunID = "other" }},
		{"unplanned repeat", func(_ []Record, e []ExternalRecord) { e[0].Repeat = 3 }},
		{"missing check", func(_ []Record, e []ExternalRecord) { e[0].Checks = e[0].Checks[:len(e[0].Checks)-1] }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			k := append([]Record(nil), kiln...)
			e := append([]ExternalRecord(nil), external...)
			tt.mutate(k, e)
			if _, err := ValidateBenchmarkResults(plan, k, e); err == nil {
				t.Fatal("invalid cohort accepted")
			}
		})
	}
}

func TestBenchmarkPlanRejectsAliasesAndMissingInputs(t *testing.T) {
	opts := BenchmarkPlanOptions{KilnBin: fakeVersionBinary(t, "kiln"), ClaudeBin: fakeVersionBinary(t, "claude"), ScenariosDir: filepath.Join("..", "..", "eval", "scenarios"), FixturesDir: filepath.Join("..", "..", "eval", "fixtures"), KilnModel: "anthropic/claude-opus-4-8", ClaudeModel: "opus", Repeats: 2, MaxTotalUSD: 25}
	if _, err := BuildBenchmarkPlan(opts); err == nil {
		t.Fatal("mutable alias accepted")
	}
	opts.ClaudeModel = "claude-opus-4-8"
	opts.MaxTotalUSD = 0
	if _, err := BuildBenchmarkPlan(opts); err == nil {
		t.Fatal("missing budget accepted")
	}
	opts.MaxTotalUSD = 25
	opts.ScenarioNames = []string{"fix-bug", "unknown"}
	if _, err := BuildBenchmarkPlan(opts); err == nil {
		t.Fatal("missing scenario accepted")
	}
}
