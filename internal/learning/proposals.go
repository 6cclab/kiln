package learning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/andrepato/harness/internal/memory"
)

// Proposal is a local, unevaluated improvement plan based only on sanitized
// run counters. It never changes runtime policy or asserts a cause.
type Proposal struct {
	ID                 string `json:"id"`
	Kind               string `json:"kind"`
	Summary            string `json:"summary"`
	Action             string `json:"action"`
	Evaluation         string `json:"evaluation"`
	ChangeType         string `json:"changeType"`
	Target             string `json:"target"`
	FromValue          int    `json:"fromValue,omitempty"`
	ToValue            int    `json:"toValue,omitempty"`
	AcceptanceCriteria string `json:"acceptanceCriteria"`
	Evidence           string `json:"evidence"`
	Source             string `json:"source"`
	SourceOutcomesHash string `json:"sourceOutcomesHash"`
	SourceRuns         int    `json:"sourceRuns"`
	AffectedRuns       int    `json:"affectedRuns"`
	SourceConfigHash   string `json:"sourceConfigHash,omitempty"`
	SourceConfigRuns   int    `json:"sourceConfigRuns,omitempty"`
	CreatedAt          int64  `json:"createdAt"`
}

// ProposalScope isolates automatic review plans from active policy and memory.
func ProposalScope(project string) string          { return "outcome-proposal:" + project }
func proposalDismissalScope(project string) string { return "outcome-proposal-dismissed:" + project }

var proposalActions = map[string]struct{ action, evaluation string }{
	"tool-failures": {
		"Inspect the failed tool results in the original sessions, reproduce a confirmed failure, and draft a targeted fix with a regression test.",
		"Compare the failure count in equivalent runs after the fix; review correctness separately. No cause is established by these counters.",
	},
	"run-failures": {
		"Inspect failed sessions, group confirmed causes, and draft a fix for one reproducible cause with a regression test.",
		"Repeat equivalent tasks and compare completion and correctness before adopting the fix. Failure status alone does not identify a cause.",
	},
	"high-input": {
		"Inspect context assembly for repeated material and draft one bounded reduction to evaluate.",
		"Run paired checked tasks with the same model, binary, and fixtures; compare quality, input tokens, cost, and latency before any policy change.",
	},
}

// RecordOutcomeAndPropose records one run and refreshes the bounded review
// queue. It makes no model calls and does not evaluate or activate a policy.
func RecordOutcomeAndPropose(ctx context.Context, store *memory.Store, project string, o Outcome, cfg ObservationConfig) error {
	cfg, err := NormalizeObservationConfig(cfg)
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		return nil
	}
	if err := RecordOutcome(ctx, store, project, o, cfg.MaxOutcomes); err != nil {
		return err
	}
	outcomes, err := ListOutcomes(ctx, store, project)
	if err != nil {
		return err
	}
	ids := make([]string, len(outcomes))
	for i, outcome := range outcomes {
		ids[i] = outcome.ID
	}
	sort.Strings(ids)
	h := sha256.New()
	for _, id := range ids {
		_, _ = h.Write([]byte(id)) // SHA-256 Hash.Write cannot fail.
	}
	sourceHash := hex.EncodeToString(h.Sum(nil))
	for _, suggestion := range Suggestions(outcomes, cfg.MinSamples) {
		dismissed, err := store.Current(ctx, proposalDismissalScope(project), suggestion.Kind, "")
		if err != nil {
			return err
		}
		if dismissed != nil {
			continue
		}
		action := proposalActions[suggestion.Kind]
		proposal := Proposal{
			ID: HashIdentity("outcome-proposal-v1\x00" + suggestion.Kind), Kind: suggestion.Kind,
			Summary: suggestion.Summary, Action: action.action, Evaluation: action.evaluation,
			Evidence: suggestion.Evidence, Source: "sanitized local main-lane run counters; hypothesis, not verified correctness",
			SourceOutcomesHash: sourceHash, SourceRuns: suggestion.Samples, AffectedRuns: suggestion.AffectedRuns,
			CreatedAt: time.Now().Unix(),
		}
		switch suggestion.Kind {
		case "tool-failures":
			proposal.ChangeType, proposal.Target = "diagnostic-fix", "tool execution"
			proposal.AcceptanceCriteria = "A reproduced failure has a regression test; equivalent runs show fewer tool failures without failed checks."
		case "run-failures":
			proposal.ChangeType, proposal.Target = "diagnostic-fix", "run completion"
			proposal.AcceptanceCriteria = "A confirmed cause has a regression test; equivalent tasks complete and pass their checks."
		case "high-input":
			proposal.ChangeType, proposal.Target = "diagnostic-review", "context assembly"
			proposal.AcceptanceCriteria = "Paired held-out checks pass without quality regression and measured cost or latency improves."
			budget := 0
			cohort := 0
			for _, outcome := range outcomes {
				if outcome.InputTokens+outcome.CacheReadTokens+outcome.CacheWriteTokens < 32000 || outcome.SkillBudgetTokens < 256 {
					continue
				}
				if budget == 0 || budget == outcome.SkillBudgetTokens {
					budget = outcome.SkillBudgetTokens
					cohort++
				}
			}
			if cohort >= cfg.MinSamples {
				proposal.ChangeType, proposal.Target = "settings-diff", "skillDiscovery.budgetTokens"
				proposal.FromValue, proposal.ToValue = budget, max(128, budget/2)
				proposal.SourceConfigHash = HashIdentity(fmt.Sprintf("skillDiscovery.budgetTokens=%d", budget))
				proposal.SourceConfigRuns = cohort
				proposal.Action = fmt.Sprintf("Evaluate skillDiscovery.budgetTokens %d -> %d in a separate candidate arm; do not apply it from these counters.", proposal.FromValue, proposal.ToValue)
				proposal.Evidence = fmt.Sprintf("%s %d high-input runs used the source budget %d; correctness and causality are unknown.", suggestion.Evidence, cohort, budget)
				proposal.Evaluation = "First collect at least 20 checked real-model source eval runs across five scenarios under an explicit source policy. Then use kiln learning queue and plan, run paired checked held-out scenarios with identical model, roles, binary, and fixtures, and use promote-candidate only if its evidence gate passes. Compare quality, tokens, cost, and latency."
			}
		}
		raw, err := json.Marshal(proposal)
		if err != nil {
			return err
		}
		if _, err := store.PutUniquePruned(ctx, memory.Record{Scope: ProposalScope(project), Key: suggestion.Kind, Kind: "procedure", Text: string(raw), Evidence: proposal.Source}, len(proposalActions)); err != nil {
			return err
		}
	}
	return nil
}

// ListProposals returns the retained review plans, newest first.
func ListProposals(ctx context.Context, store *memory.Store, project string) ([]Proposal, error) {
	records, err := store.Recent(ctx, ProposalScope(project), len(proposalActions))
	if err != nil {
		return nil, err
	}
	out := make([]Proposal, 0, len(records))
	for _, record := range records {
		var p Proposal
		if err := json.Unmarshal([]byte(record.Text), &p); err != nil {
			return nil, err
		}
		if !suggestionKinds[p.Kind] || p.ID != HashIdentity("outcome-proposal-v1\x00"+p.Kind) || !validDigest(p.SourceOutcomesHash) || p.SourceRuns < 2 || p.AffectedRuns > p.SourceRuns || p.ChangeType == "" || p.Target == "" || p.AcceptanceCriteria == "" {
			return nil, fmt.Errorf("invalid stored outcome proposal")
		}
		out = append(out, p)
	}
	return out, nil
}

// DismissProposal removes one plan and suppresses its kind until outcomes are forgotten.
func DismissProposal(ctx context.Context, store *memory.Store, project, id string) error {
	kind := ""
	for candidate := range proposalActions {
		if id == HashIdentity("outcome-proposal-v1\x00"+candidate) {
			kind = candidate
			break
		}
	}
	if kind == "" {
		return fmt.Errorf("unknown outcome proposal ID")
	}
	if _, err := store.PutUniquePruned(ctx, memory.Record{Scope: proposalDismissalScope(project), Key: kind, Kind: "procedure", Text: "dismissed", Evidence: "Explicit user command"}, len(proposalActions)); err != nil {
		return err
	}
	return store.Forget(ctx, ProposalScope(project), kind)
}
