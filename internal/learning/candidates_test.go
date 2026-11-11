package learning

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/memory"
)

func candidateResults(t *testing.T, p Policy) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "results.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for task := 0; task < 5; task++ {
		for repeat := 0; repeat < 4; repeat++ {
			r := Result{RunID: "source", Scenario: fmt.Sprintf("source-%d", task), Provider: "real", Model: "m", Version: "v1", BinaryHash: "binary", Repeat: repeat, DatasetHash: fmt.Sprintf("dataset-%d", task), FixtureHash: fmt.Sprintf("fixture-%d", task), PolicyHash: p.Hash(), OK: true, Score: 1, CostUSD: 0.01, DurationMS: 1000}
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
	return path
}

func TestQueueCandidateProvenanceIdempotenceAndIsolation(t *testing.T) {
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
	p := Policy{SkillBudgetTokens: 1024, ConservativeRouting: true}
	path := candidateResults(t, p)
	c, err := QueueFromResults(ctx, store, "a", path, p, 1)
	if err != nil {
		t.Fatal(err)
	}
	if c.ID == "" || c.Policy.SkillBudgetTokens != 512 || c.SourcePolicyHash != p.Hash() || c.SourceResultsHash == "" || c.SourceRunID != "source" || c.SourceBinaryHash != "binary" || c.SourceModel != "real/m" || len(c.SourceScenarioNames) != 5 || len(c.SourceDatasets) != 5 || c.Plan.Pairs != 20 || c.Plan.TotalRuns != 40 || math.Abs(c.Plan.EstimatedUSD-0.4) > 1e-9 {
		t.Fatalf("candidate provenance or plan: %+v", c)
	}
	if c2, err := QueueFromResults(ctx, store, "a", path, p, 1); err != nil || c2.QueuedAt != c.QueuedAt {
		t.Fatalf("duplicate queue changed candidate: %+v %v", c2, err)
	}
	list, err := ListCandidates(ctx, store, "a")
	if err != nil || len(list) != 1 {
		t.Fatalf("duplicates: %+v %v", list, err)
	}
	list, err = ListCandidates(ctx, store, "b")
	if err != nil || len(list) != 0 {
		t.Fatalf("cross-project candidates: %+v %v", list, err)
	}
	active, err := Load(ctx, store, "a")
	if err != nil || active != nil {
		t.Fatalf("unevaluated candidate became active: %+v %v", active, err)
	}
	if err := DiscardCandidate(ctx, store, "a", c.ID); err != nil {
		t.Fatal(err)
	}
	list, err = ListCandidates(ctx, store, "a")
	if err != nil || len(list) != 0 {
		t.Fatalf("discard did not remove candidate: %+v %v", list, err)
	}
}

func TestQueueCandidateBudgetAndSourceChecks(t *testing.T) {
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
	p := Policy{SkillBudgetTokens: 1024}
	path := candidateResults(t, p)
	for _, tt := range []struct {
		name   string
		policy Policy
		budget float64
	}{
		{"cost budget", p, 0.1},
		{"source policy mismatch", Policy{SkillBudgetTokens: 2048}, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := QueueFromResults(ctx, store, "a", path, tt.policy, tt.budget); err == nil {
				t.Fatal("invalid candidate queued")
			}
		})
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	badPath := filepath.Join(t.TempDir(), "heldout.jsonl")
	bad := []byte(strings.Replace(string(raw), `"tags":null`, `"tags":["holdout"]`, 1))
	if err := os.WriteFile(badPath, bad, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := QueueFromResults(ctx, store, "a", badPath, p, 1); err == nil {
		t.Fatal("held-out source scenario accepted")
	}
	list, err := ListCandidates(ctx, store, "a")
	if err != nil || len(list) != 0 {
		t.Fatalf("invalid candidates persisted: %+v %v", list, err)
	}
}
