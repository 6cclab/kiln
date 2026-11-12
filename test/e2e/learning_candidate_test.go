//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/andrepato/harness/internal/learning"
)

func TestLearningCandidateCLIPlanAndDiscard(t *testing.T) {
	home, sessDir := scratchHome(t)
	project := scratchProject(t)
	env := baseEnv(home, sessDir, "")
	root := t.TempDir()
	policy := learning.Policy{SkillBudgetTokens: 1024, ConservativeRouting: true}
	policyFile := filepath.Join(root, "source-policy.json")
	rawPolicy, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyFile, rawPolicy, 0600); err != nil {
		t.Fatal(err)
	}
	results := filepath.Join(root, "source.jsonl")
	f, err := os.Create(results)
	if err != nil {
		t.Fatal(err)
	}
	for task := 0; task < 5; task++ {
		for repeat := 1; repeat <= 4; repeat++ {
			r := learning.Result{RunID: "source", Scenario: fmt.Sprintf("source-%d", task), Provider: "real", Model: "m", Version: "v1", BinaryHash: "binary", Repeat: repeat, DatasetHash: fmt.Sprintf("dataset-%d", task), FixtureHash: fmt.Sprintf("fixture-%d", task), PolicyHash: policy.Hash(), OK: true, Score: 1, CostUSD: 0.01, DurationMS: 1000}
			r.Usage.Input = 5000
			r.Checks = append(r.Checks, struct {
				Pass    bool `json:"pass"`
				Skipped bool `json:"skipped"`
			}{Pass: true})
			if err := json.NewEncoder(f).Encode(r); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	queue := runHarness(t, project, env, "learning", "queue", policyFile, results)
	if queue.Code != 0 {
		t.Fatalf("queue: %s", queue.Stderr)
	}
	var c learning.Candidate
	if err := json.Unmarshal([]byte(queue.Stdout), &c); err != nil {
		t.Fatal(err)
	}
	if c.ID == "" {
		t.Fatal("candidate ID absent")
	}
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
		definition := fmt.Sprintf("name: %s\ntags: [holdout]\nfixture: %s\nprompt: Fix task %d\nchecks:\n  - type: result_ok\n", name, name, i)
		if err := os.WriteFile(filepath.Join(scenarios, name, "scenario.yaml"), []byte(definition), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixtures, name, "data.txt"), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	plan := runHarness(t, project, env, "learning", "plan", c.ID, scenarios, fixtures)
	if plan.Code != 0 {
		t.Fatalf("plan: %s", plan.Stderr)
	}
	var manifest learning.Manifest
	if err := json.Unmarshal([]byte(plan.Stdout), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.CandidateID != c.ID || manifest.TotalRuns != 40 || len(manifest.ExactNames) != 5 {
		t.Fatalf("incomplete manifest: %+v", manifest)
	}
	discard := runHarness(t, project, env, "learning", "discard", c.ID)
	if discard.Code != 0 {
		t.Fatalf("discard: %s", discard.Stderr)
	}
	missing := runHarness(t, project, env, "learning", "plan", c.ID, scenarios, fixtures)
	if missing.Code == 0 {
		t.Fatal("discarded candidate could still be planned")
	}
}
