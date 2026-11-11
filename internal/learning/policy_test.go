package learning

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/andrepato/harness/internal/memory"
)

func sampleEvidence(p Policy) ([]Result, []Result) {
	var baseline, candidate []Result
	for task := 0; task < 5; task++ {
		for repeat := 0; repeat < 4; repeat++ {
			r := Result{RunID: "baseline", Scenario: fmt.Sprintf("task-%d", task), Provider: "test", Model: "model", Version: "v1", BinaryHash: "binary", Repeat: repeat, DatasetHash: fmt.Sprintf("fixture-%d", task), Tags: []string{"holdout"}, OK: true, Score: 1, CostUSD: 0.02, DurationMS: 1000}
			r.Usage.Input = 100
			r.Usage.Output = 10
			r.Checks = append(r.Checks, struct {
				Pass    bool `json:"pass"`
				Skipped bool `json:"skipped"`
			}{Pass: true})
			baseline = append(baseline, r)
			r.RunID = "candidate"
			r.PolicyHash = p.Hash()
			r.Usage.Input = 50
			r.CostUSD = 0.01
			candidate = append(candidate, r)
		}
	}
	return baseline, candidate
}

func TestPolicyGate(t *testing.T) {
	p := Policy{SkillBudgetTokens: 512, ConservativeRouting: true}
	tests := []struct {
		name     string
		mutate   func([]Result)
		accepted bool
	}{
		{"improved", func([]Result) {}, true},
		{"quality regression", func(r []Result) { r[0].Score = 0.5 }, false},
		{"mismatched policy", func(r []Result) { r[0].PolicyHash = "wrong" }, false},
		{"fixture changed", func(r []Result) { r[0].DatasetHash = "new" }, false},
		{"binary changed", func(r []Result) { r[0].BinaryHash = "new" }, false},
		{"not held out", func(r []Result) { r[0].Tags = nil }, false},
		{"faux", func(r []Result) { r[0].Provider = "faux" }, false},
		{"duplicate", func(r []Result) { r[1] = r[0] }, false},
		{"no measurable improvement", func(r []Result) {
			for i := range r {
				r[i].Usage.Input = 100
				r[i].CostUSD = 0.02
			}
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, b := sampleEvidence(p)
			tt.mutate(b)
			got := Evaluate(p, a, b)
			if got.Accepted != tt.accepted {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestPolicyPromoteAndRollback(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	store, err := memory.Open(filepath.Join(root, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	write := func(name string, results []Result) string {
		t.Helper()
		path := filepath.Join(root, name)
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range results {
			if err := json.NewEncoder(f).Encode(r); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		return path
	}
	p := Policy{SkillBudgetTokens: 512, ConservativeRouting: true}
	a, b := sampleEvidence(p)
	baseline, candidate := write("baseline.jsonl", a), write("candidate.jsonl", b)
	if report, err := Promote(ctx, store, "project", p, baseline, candidate); err != nil || !report.Accepted {
		t.Fatalf("%+v %v", report, err)
	}
	active, err := Load(ctx, store, "project")
	if err != nil || active == nil || active.Policy != p {
		t.Fatalf("%+v %v", active, err)
	}
	records, err := store.Search(ctx, Scope("project"), "active", 1)
	if err != nil {
		t.Fatal(err)
	}
	oldID := records[0].ID
	oldHash := p.Hash()
	p.SkillBudgetTokens = 256
	a, b = sampleEvidence(p)
	for i := range a {
		a[i].PolicyHash = oldHash
	}
	if _, err := Promote(ctx, store, "project", p, write("baseline2.jsonl", a), write("candidate2.jsonl", b)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Restore(ctx, Scope("project"), oldID); err != nil {
		t.Fatal(err)
	}
	active, err = Load(ctx, store, "project")
	if err != nil || active.Policy.SkillBudgetTokens != 512 {
		t.Fatalf("rollback: %+v %v", active, err)
	}
	other, err := Load(ctx, store, "another-project")
	if err != nil || other != nil {
		t.Fatal("cross-project policy")
	}
}

func TestReadPolicyRejectsPermissionOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(`{"skillBudgetTokens":512,"permissions":{"defaultMode":"bypassPermissions"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPolicy(path); err == nil {
		t.Fatal("policy changed permissions")
	}
}
