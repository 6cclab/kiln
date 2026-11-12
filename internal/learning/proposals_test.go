package learning

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/memory"
)

func TestAutomaticProposalsAreBoundedReviewableAndDismissible(t *testing.T) {
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
	cfg := ObservationConfig{Enabled: true, MaxOutcomes: 5, MinSamples: 3}
	for i := 0; i < 5; i++ {
		o := sampleOutcome(string(rune('a' + i)))
		o.Status = "failed"
		o.ToolFailure = 1
		o.BashFailure = 1
		o.InputTokens = 40000
		if err := RecordOutcomeAndPropose(ctx, store, "project", o, cfg); err != nil {
			t.Fatal(err)
		}
	}
	proposals, err := ListProposals(ctx, store, "project")
	if err != nil || len(proposals) != 3 {
		t.Fatalf("proposals: %+v %v", proposals, err)
	}
	for _, p := range proposals {
		if p.Action == "" || p.Evaluation == "" || p.SourceOutcomesHash == "" || p.SourceRuns < 3 || p.AffectedRuns < 2 || !strings.Contains(p.Source, "hypothesis") {
			t.Fatalf("incomplete review plan: %+v", p)
		}
	}
	if err := RecordOutcomeAndPropose(ctx, store, "project", sampleOutcome("a"), cfg); err != nil {
		t.Fatal(err)
	}
	proposals, err = ListProposals(ctx, store, "project")
	if err != nil || len(proposals) != 3 {
		t.Fatalf("duplicate proposals: %+v %v", proposals, err)
	}
	if err := DismissProposal(ctx, store, "project", proposals[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := DismissProposal(ctx, store, "project", "unknown"); err == nil {
		t.Fatal("accepted unknown proposal")
	}
	if err := RecordOutcomeAndPropose(ctx, store, "project", sampleOutcome("f"), cfg); err != nil {
		t.Fatal(err)
	}
	proposals, err = ListProposals(ctx, store, "project")
	if err != nil || len(proposals) != 2 {
		t.Fatalf("dismissed proposal regenerated: %+v %v", proposals, err)
	}
	if err := ForgetOutcomes(ctx, store, "project"); err != nil {
		t.Fatal(err)
	}
	proposals, err = ListProposals(ctx, store, "project")
	if err != nil || len(proposals) != 0 {
		t.Fatalf("forget left proposals: %+v %v", proposals, err)
	}
	other, err := ListProposals(ctx, store, "other")
	if err != nil || len(other) != 0 {
		t.Fatalf("project scope leaked: %+v %v", other, err)
	}
}

func TestDisabledAutomaticProposalsDoNotWrite(t *testing.T) {
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
	if err := RecordOutcomeAndPropose(ctx, store, "project", sampleOutcome("secret"), ObservationConfig{}); err != nil {
		t.Fatal(err)
	}
	outcomes, err := ListOutcomes(ctx, store, "project")
	if err != nil || len(outcomes) != 0 {
		t.Fatalf("disabled capture wrote: %+v %v", outcomes, err)
	}
}

func TestHighInputProposalHasSourceBoundSettingsDiff(t *testing.T) {
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
	cfg := ObservationConfig{Enabled: true, MaxOutcomes: 5, MinSamples: 3, SourceSkillBudgetTokens: 512}
	for i := 0; i < 3; i++ {
		o := sampleOutcome(string(rune('a' + i)))
		o.InputTokens = 40000
		o.SkillBudgetTokens = cfg.SourceSkillBudgetTokens
		if err := RecordOutcomeAndPropose(ctx, store, "project", o, cfg); err != nil {
			t.Fatal(err)
		}
	}
	proposals, err := ListProposals(ctx, store, "project")
	if err != nil || len(proposals) != 1 {
		t.Fatalf("proposals: %+v %v", proposals, err)
	}
	p := proposals[0]
	if p.ChangeType != "settings-diff" || p.Target != "skillDiscovery.budgetTokens" || p.FromValue != 512 || p.ToValue != 256 || !validDigest(p.SourceConfigHash) || !strings.Contains(p.Evaluation, "held-out") {
		t.Fatalf("budget proposal lacks a reviewable source-bound diff: %+v", p)
	}
	if p.SourceOutcomesHash == p.SourceConfigHash {
		t.Fatal("cohort and configuration provenance conflated")
	}
}

func TestReadinessProposalRefreshesRetainedCohortAndBudget(t *testing.T) {
	ctx := context.Background()
	store, err := memory.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := ObservationConfig{Enabled: true, MaxOutcomes: 3, MinSamples: 3, SourceSkillBudgetTokens: 512}
	for i := 0; i < 3; i++ {
		o := sampleOutcome(string(rune('a' + i)))
		o.InputTokens, o.SkillBudgetTokens = 40000, 512
		if err := RecordOutcomeAndPropose(ctx, store, "project", o, cfg); err != nil {
			t.Fatal(err)
		}
	}
	first, err := ListProposals(ctx, store, "project")
	if err != nil || len(first) != 1 || first[0].FromValue != 512 {
		t.Fatalf("first proposal: %+v %v", first, err)
	}
	cfg.SourceSkillBudgetTokens = 1024
	for i := 0; i < 3; i++ {
		o := sampleOutcome(string(rune('d' + i)))
		o.InputTokens, o.SkillBudgetTokens = 40000, 1024
		if err := RecordOutcomeAndPropose(ctx, store, "project", o, cfg); err != nil {
			t.Fatal(err)
		}
	}
	second, err := ListProposals(ctx, store, "project")
	if err != nil || len(second) != 1 || second[0].FromValue != 1024 || second[0].ToValue != 512 || second[0].SourceOutcomesHash == first[0].SourceOutcomesHash || second[0].SourceConfigHash == first[0].SourceConfigHash || second[0].ID != first[0].ID {
		t.Fatalf("proposal did not refresh retained cohort and budget: first=%+v second=%+v err=%v", first, second, err)
	}
	if err := DismissProposal(ctx, store, "project", second[0].ID); err != nil {
		t.Fatal(err)
	}
	o := sampleOutcome("g")
	o.InputTokens, o.SkillBudgetTokens = 40000, 1024
	if err := RecordOutcomeAndPropose(ctx, store, "project", o, cfg); err != nil {
		t.Fatal(err)
	}
	afterDismissal, err := ListProposals(ctx, store, "project")
	if err != nil || len(afterDismissal) != 0 {
		t.Fatalf("explicit dismissal was lost: %+v %v", afterDismissal, err)
	}
}

func TestReadinessProposalWithdrawsWithoutSupportingCohort(t *testing.T) {
	ctx := context.Background()
	store, err := memory.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := ObservationConfig{Enabled: true, MaxOutcomes: 3, MinSamples: 3, SourceSkillBudgetTokens: 512}
	for i := 0; i < 3; i++ {
		o := sampleOutcome(string(rune('a' + i)))
		o.InputTokens, o.SkillBudgetTokens = 40000, 512
		if err := RecordOutcomeAndPropose(ctx, store, "project", o, cfg); err != nil {
			t.Fatal(err)
		}
	}
	initial, err := ListProposals(ctx, store, "project")
	if err != nil || len(initial) != 1 {
		t.Fatalf("initial cohort: %+v %v", initial, err)
	}
	for i := 0; i < 3; i++ {
		if err := RecordOutcomeAndPropose(ctx, store, "project", sampleOutcome(string(rune('d'+i))), cfg); err != nil {
			t.Fatal(err)
		}
	}
	withdrawn, err := ListProposals(ctx, store, "project")
	if err != nil || len(withdrawn) != 0 {
		t.Fatalf("unsupported proposal remained active: %+v %v", withdrawn, err)
	}
}
