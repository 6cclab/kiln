package learning

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/memory"
)

func setupHeldOut(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	scenarios := filepath.Join(root, "scenarios")
	fixtures := filepath.Join(root, "fixtures")
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("held-%d", i)
		if err := os.MkdirAll(filepath.Join(scenarios, name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(fixtures, name), 0700); err != nil {
			t.Fatal(err)
		}
		yaml := fmt.Sprintf("name: %s\ntags: [holdout]\nfixture: %s\nprompt: Fix task %d\nchecks:\n  - type: result_ok\n", name, name, i)
		if err := os.WriteFile(filepath.Join(scenarios, name, "scenario.yaml"), []byte(yaml), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixtures, name, "data.txt"), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return scenarios, fixtures
}

func queueForManifest(t *testing.T, store *memory.Store) (Candidate, string) {
	t.Helper()
	p := Policy{SkillBudgetTokens: 1024, ConservativeRouting: true}
	source := candidateResults(t, p)
	c, err := QueueFromResults(context.Background(), store, "project", source, p, 1)
	if err != nil {
		t.Fatal(err)
	}
	return c, source
}

func writeArm(t *testing.T, dir, name, runID string, c Candidate, m Manifest, candidate bool) string {
	t.Helper()
	path := filepath.Join(dir, name+".jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range m.Scenarios {
		for repeat := 1; repeat <= 4; repeat++ {
			r := Result{RunID: runID, Scenario: sc.Name, Tags: []string{"holdout"}, Provider: "real", Model: "m", Version: c.SourceVersion, BinaryHash: c.SourceBinaryHash, Repeat: repeat, DatasetHash: sc.DatasetHash, FixtureHash: sc.FixtureHash, PolicyHash: c.SourcePolicyHash, Roles: c.SourceRoles, OK: true, Score: 1, CostUSD: 0.02, DurationMS: 1000}
			r.Usage.Input = 100
			r.Usage.Output = 10
			r.Checks = append(r.Checks, struct {
				Pass    bool `json:"pass"`
				Skipped bool `json:"skipped"`
			}{Pass: true})
			if candidate {
				r.PolicyHash = c.Policy.Hash()
				r.Usage.Input = 50
				r.CostUSD = 0.01
			}
			if err := json.NewEncoder(f).Encode(r); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCandidatePlanAndPromotion(t *testing.T) {
	ctx := context.Background()
	store, err := memory.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	c, _ := queueForManifest(t, store)
	scenarios, fixtures := setupHeldOut(t)
	m, err := PlanCandidate(ctx, store, "project", c.ID, scenarios, fixtures)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Scenarios) != 5 || m.BaselinePolicy.Hash() != c.SourcePolicyHash || m.CandidatePolicy.Hash() != c.Policy.Hash() || m.Model != c.SourceModel || m.TotalRuns != 40 {
		t.Fatalf("incomplete plan: %+v", m)
	}
	baseline := writeArm(t, t.TempDir(), "baseline", "baseline", c, m, false)
	candidate := writeArm(t, t.TempDir(), "candidate", "candidate", c, m, true)
	report, err := PromoteCandidate(ctx, store, "project", c.ID, baseline, candidate)
	if err != nil || !report.Accepted {
		t.Fatalf("promotion: %+v %v", report, err)
	}
	active, err := Load(ctx, store, "project")
	if err != nil || active == nil || active.Policy.Hash() != c.Policy.Hash() {
		t.Fatalf("active: %+v %v", active, err)
	}
}

func TestCandidatePlanRejectsOverlapAndChangedFixture(t *testing.T) {
	ctx := context.Background()
	store, err := memory.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	c, _ := queueForManifest(t, store)
	scenarios, fixtures := setupHeldOut(t)
	// A renamed scenario with a source fixture identity still overlaps.
	root := filepath.Join(fixtures, "held-0")
	fixtureHash, err := Fingerprint(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.SourceFixtures["source-0"] = fixtureHash
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, memory.Record{Scope: CandidateScope("project"), Key: c.ID, Kind: "procedure", Text: string(raw), Evidence: "test source fixture overlap"}); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanCandidate(ctx, store, "project", c.ID, scenarios, fixtures); err == nil {
		t.Fatal("overlapping fixture accepted as holdout")
	}
	delete(c.SourceFixtures, "source-0")
	c.SourceFixtures["source-0"] = "fixture-0"
	raw, err = json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, memory.Record{Scope: CandidateScope("project"), Key: c.ID, Kind: "procedure", Text: string(raw), Evidence: "test restored source metadata"}); err != nil {
		t.Fatal(err)
	}
	m, err := PlanCandidate(ctx, store, "project", c.ID, scenarios, fixtures)
	if err != nil {
		t.Fatal(err)
	}
	baseline := writeArm(t, t.TempDir(), "baseline", "baseline", c, m, false)
	candidate := writeArm(t, t.TempDir(), "candidate", "candidate", c, m, true)
	if err := os.WriteFile(filepath.Join(fixtures, "held-0", "data.txt"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PromoteCandidate(ctx, store, "project", c.ID, baseline, candidate); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed fixture accepted: %v", err)
	}
	active, err := Load(ctx, store, "project")
	if err != nil || active != nil {
		t.Fatalf("failed promotion changed active: %+v %v", active, err)
	}
}

func TestCandidatePromotionRejectsWrongMetadata(t *testing.T) {
	ctx := context.Background()
	store, err := memory.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	c, _ := queueForManifest(t, store)
	scenarios, fixtures := setupHeldOut(t)
	m, err := PlanCandidate(ctx, store, "project", c.ID, scenarios, fixtures)
	if err != nil {
		t.Fatal(err)
	}
	baseline := writeArm(t, t.TempDir(), "baseline", "baseline", c, m, false)
	candidate := writeArm(t, t.TempDir(), "candidate", "candidate", c, m, true)
	raw, err := os.ReadFile(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, []byte(strings.Replace(string(raw), `"binary_hash":"binary"`, `"binary_hash":"other"`, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PromoteCandidate(ctx, store, "project", c.ID, baseline, candidate); err == nil {
		t.Fatal("wrong binary promoted")
	}
	active, err := Load(ctx, store, "project")
	if err != nil || active != nil {
		t.Fatalf("failed promotion changed active: %+v %v", active, err)
	}
}

func TestCandidatePlanRejectsPinnedModelAndLateDuplicate(t *testing.T) {
	ctx := context.Background()
	store, err := memory.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	c, _ := queueForManifest(t, store)
	scenarios, fixtures := setupHeldOut(t)
	path := filepath.Join(scenarios, "held-0", "scenario.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(raw), "tags: [holdout]", "tags: [holdout, 'pin-model:other/model']", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanCandidate(ctx, store, "project", c.ID, scenarios, fixtures); err == nil {
		t.Fatal("pinned model mismatch accepted")
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ name, replacement string }{
		{"duplicate matching pins", "tags: [holdout, 'pin-model:real/m', 'pin-model:real/m']"},
		{"comma name", "name: 'held,0'"},
		{"leading whitespace name", "name: ' held-0'"},
		{"trailing whitespace name", "name: 'held-0 '"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			changed := string(raw)
			if strings.HasPrefix(tt.replacement, "tags:") {
				changed = strings.Replace(changed, "tags: [holdout]", tt.replacement, 1)
			} else {
				changed = strings.Replace(changed, "name: held-0", tt.replacement, 1)
			}
			if err := os.WriteFile(path, []byte(changed), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := PlanCandidate(ctx, store, "project", c.ID, scenarios, fixtures); err == nil {
				t.Fatal("invalid exact-name or model pins accepted")
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
	m, err := PlanCandidate(ctx, store, "project", c.ID, scenarios, fixtures)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(raw), "tags: [holdout]", "tags: [holdout, 'pin-model:real/m', 'pin-model:real/m']", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateManifest(c, m); err == nil {
		t.Fatal("duplicate model pins accepted during revalidation")
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	// List sorts by scenario name. A duplicate encountered after the fifth
	// selected task must still invalidate the plan.
	extra := filepath.Join(scenarios, "zz-extra")
	if err := os.MkdirAll(extra, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extra, "scenario.yaml"), []byte("name: held-0\ntags: [holdout]\nprompt: duplicate\nchecks:\n  - type: result_ok\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanCandidate(ctx, store, "project", c.ID, scenarios, fixtures); err == nil {
		t.Fatal("late duplicate name accepted")
	}
}

func TestCandidatePlanPathsSurviveDifferentWorkingDirectory(t *testing.T) {
	ctx := context.Background()
	store, err := memory.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	c, _ := queueForManifest(t, store)
	scenarios, fixtures := setupHeldOut(t)
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Dir(scenarios)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(original); err != nil {
			t.Error(err)
		}
	})
	m, err := PlanCandidate(ctx, store, "project", c.ID, "scenarios", "fixtures")
	if err != nil {
		t.Fatal(err)
	}
	canonicalScenarios, err := filepath.EvalSymlinks(scenarios)
	if err != nil {
		t.Fatal(err)
	}
	canonicalFixtures, err := filepath.EvalSymlinks(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(m.ScenarioDir) || !filepath.IsAbs(m.FixturesDir) || m.ScenarioDir != canonicalScenarios || m.FixturesDir != canonicalFixtures {
		t.Fatalf("noncanonical manifest paths: %+v", m)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := validateManifest(c, m); err != nil {
		t.Fatalf("plan depends on caller cwd: %v", err)
	}
}

func TestCandidateRequiresFullSourcePolicyAndPlan(t *testing.T) {
	ctx := context.Background()
	store, err := memory.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	c, _ := queueForManifest(t, store)
	if _, err := PromoteCandidate(ctx, store, "project", c.ID, "baseline.jsonl", "candidate.jsonl"); err == nil || !strings.Contains(err.Error(), "no validated plan") {
		t.Fatalf("unplanned candidate promoted: %v", err)
	}
	c.SourcePolicy = Policy{}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, memory.Record{Scope: CandidateScope("project"), Key: c.ID, Kind: "procedure", Text: string(raw), Evidence: "legacy hash-only candidate"}); err != nil {
		t.Fatal(err)
	}
	scenarios, fixtures := setupHeldOut(t)
	if _, err := PlanCandidate(ctx, store, "project", c.ID, scenarios, fixtures); err == nil || !strings.Contains(err.Error(), "provenance") {
		t.Fatalf("legacy candidate accepted: %v", err)
	}
}
