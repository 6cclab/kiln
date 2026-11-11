package learning

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"time"

	"github.com/andrepato/harness/internal/diag"
	"github.com/andrepato/harness/internal/memory"
)

// Candidate is an unevaluated, reversible policy hypothesis and its evidence.
// A queued candidate is never applied by Load or Promote.
type Candidate struct {
	ID                  string            `json:"id"`
	Policy              Policy            `json:"policy"`
	SourcePolicy        Policy            `json:"sourcePolicy"`
	SourcePolicyHash    string            `json:"sourcePolicyHash"`
	SourceResultsHash   string            `json:"sourceResultsHash"`
	SourceRunID         string            `json:"sourceRunID"`
	SourceBinaryHash    string            `json:"sourceBinaryHash"`
	SourceVersion       string            `json:"sourceVersion"`
	SourceModel         string            `json:"sourceModel"`
	SourceRoles         map[string]string `json:"sourceRoles"`
	SourceRuns          int               `json:"sourceRuns"`
	SourceScenarios     int               `json:"sourceScenarios"`
	SourceScenarioNames []string          `json:"sourceScenarioNames"`
	SourceDatasets      map[string]string `json:"sourceDatasets"`
	SourceFixtures      map[string]string `json:"sourceFixtures"`
	SourceMeanInput     float64           `json:"sourceMeanInputTokens"`
	Reason              string            `json:"reason"`
	Plan                EvaluationPlan    `json:"plan"`
	QueuedAt            int64             `json:"queuedAt"`
}

// EvaluationPlan is a cost estimate and required evidence, not an execution job.
type EvaluationPlan struct {
	Pairs                   int     `json:"pairs"`
	TotalRuns               int     `json:"totalRuns"`
	MinimumHeldOutScenarios int     `json:"minimumHeldOutScenarios"`
	EstimatedUSD            float64 `json:"estimatedUSD"`
	MaximumEstimatedUSD     float64 `json:"maximumEstimatedUSD"`
	Assumptions             string  `json:"assumptions"`
	Status                  string  `json:"status"`
}

// CandidateScope keeps hypotheses separate from active policy and conversation memory.
func CandidateScope(project string) string { return "policy-candidate:" + project }

// QueueFromResults proposes one smaller skill budget from successful, non-held-out
// real-model outcomes. It stores an offline evaluation plan and makes no model calls.
func QueueFromResults(ctx context.Context, store *memory.Store, project, resultsPath string, source Policy, maxEstimatedUSD float64) (Candidate, error) {
	var candidate Candidate
	if err := source.Validate(); err != nil {
		return candidate, err
	}
	if source.SkillBudgetTokens <= 128 {
		return candidate, fmt.Errorf("skill budget is already at its minimum")
	}
	if maxEstimatedUSD <= 0 || math.IsNaN(maxEstimatedUSD) || math.IsInf(maxEstimatedUSD, 0) {
		return candidate, fmt.Errorf("maximum estimated cost must be positive and finite")
	}
	f, err := os.Open(resultsPath)
	if err != nil {
		return candidate, err
	}
	defer diag.Close(f)
	data, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	if err != nil {
		return candidate, err
	}
	if len(data) > 8<<20 {
		return candidate, fmt.Errorf("source results exceed 8 MiB")
	}
	results, err := readResultsReader(bytes.NewReader(data))
	if err != nil {
		return candidate, err
	}
	if len(results) < 20 || len(results) > 1000 {
		return candidate, fmt.Errorf("source requires 20 to 1000 recorded runs")
	}
	first := results[0]
	if first.Provider == "" || first.Provider == "faux" || first.Model == "" || first.BinaryHash == "" || first.Version == "" || first.RunID == "" || len(first.RunID) > 256 || first.PolicyHash != source.Hash() || (first.Provider == "kiln" && first.Model == "auto" && len(first.Roles) == 0) {
		return candidate, fmt.Errorf("source lacks real-model binary, model, version or policy provenance")
	}
	seen := map[string]bool{}
	scenarios := map[string]bool{}
	datasets := map[string]string{}
	fixtures := map[string]string{}
	var input, cost float64
	for _, r := range results {
		if r.RunID != first.RunID || r.Provider != first.Provider || r.Model != first.Model || r.BinaryHash != first.BinaryHash || r.Version != first.Version || r.PolicyHash != first.PolicyHash || !SameRoles(r.Roles, first.Roles) {
			return candidate, fmt.Errorf("source mixes model, binary, roles or policy")
		}
		if r.RunID == "" || r.Scenario == "" || len(r.Scenario) > 128 || r.DatasetHash == "" || r.FixtureHash == "" || heldOut(r) || !r.OK || !checked(r) || r.Score < 0 || r.Score > 1 || math.IsNaN(r.Score) || r.CostUSD <= 0 || math.IsNaN(r.CostUSD) || math.IsInf(r.CostUSD, 0) || r.DurationMS <= 0 || r.Usage.Input <= 0 {
			return candidate, fmt.Errorf("source contains incomplete, failed, held-out or invalid outcome")
		}
		if prior, ok := datasets[r.Scenario]; ok && prior != r.DatasetHash {
			return candidate, fmt.Errorf("source scenario changes fixture fingerprint")
		}
		if prior, ok := fixtures[r.Scenario]; ok && prior != r.FixtureHash {
			return candidate, fmt.Errorf("source scenario changes fixture identity")
		}
		datasets[r.Scenario] = r.DatasetHash
		fixtures[r.Scenario] = r.FixtureHash
		identity := key(r)
		if seen[identity] {
			return candidate, fmt.Errorf("source contains duplicate run")
		}
		seen[identity] = true
		scenarios[r.Scenario] = true
		input += float64(r.Usage.Input)
		cost += r.CostUSD
	}
	if len(scenarios) < 5 {
		return candidate, fmt.Errorf("source requires five distinct scenarios")
	}
	if len(scenarios) > 20 {
		return candidate, fmt.Errorf("source exceeds 20 distinct scenarios in one plan")
	}
	scenarioNames := make([]string, 0, len(scenarios))
	for name := range scenarios {
		scenarioNames = append(scenarioNames, name)
	}
	sort.Strings(scenarioNames)
	meanInput := input / float64(len(results))
	if meanInput < float64(source.SkillBudgetTokens*2) {
		return candidate, fmt.Errorf("source input usage is too small to justify a discovery budget hypothesis")
	}
	policy := source
	policy.SkillBudgetTokens = max(128, source.SkillBudgetTokens/2)
	if err := policy.Validate(); err != nil {
		return candidate, err
	}
	// Two arms, 20 matched pairs. This is an estimate only; the evaluation
	// runner is not invoked and actual spend is controlled by its operator.
	estimate := 40 * cost / float64(len(results))
	if estimate > maxEstimatedUSD {
		return candidate, fmt.Errorf("estimated paired evaluation cost $%.4f exceeds the queue budget $%.4f", estimate, maxEstimatedUSD)
	}
	digest := sha256.Sum256(data)
	hash := hex.EncodeToString(digest[:])
	candidate = Candidate{
		Policy: policy, SourcePolicy: source, SourcePolicyHash: source.Hash(), SourceResultsHash: hash, SourceRunID: first.RunID,
		SourceBinaryHash: first.BinaryHash, SourceVersion: first.Version,
		SourceModel: first.Provider + "/" + first.Model, SourceRoles: first.Roles,
		SourceRuns: len(results), SourceScenarios: len(scenarios), SourceScenarioNames: scenarioNames, SourceDatasets: datasets, SourceFixtures: fixtures, SourceMeanInput: meanInput,
		Reason: "Successful source runs have high input use; a smaller skill discovery budget may reduce cost without reducing quality.",
		Plan: EvaluationPlan{Pairs: 20, TotalRuns: 40, MinimumHeldOutScenarios: 5, EstimatedUSD: estimate, MaximumEstimatedUSD: maxEstimatedUSD,
			Assumptions: "40 runs at the source mean cost. Use five or more held-out scenarios absent from the source results, four repeats each, identical model, roles, binary and fixtures in both arms. Estimate is not an enforceable spend cap.", Status: "queued; no runs started"},
	}
	idHash := sha256.Sum256([]byte(candidate.SourceResultsHash + "\x00" + candidate.Policy.Hash()))
	key := hex.EncodeToString(idHash[:])
	if existing, err := store.Current(ctx, CandidateScope(project), key, ""); err != nil {
		return Candidate{}, err
	} else if existing != nil {
		if err := json.Unmarshal([]byte(existing.Text), &candidate); err != nil {
			return Candidate{}, err
		}
		return candidate, nil
	}
	candidate.ID = key
	candidate.QueuedAt = time.Now().Unix()
	raw, err := json.Marshal(candidate)
	if err != nil {
		return Candidate{}, err
	}
	_, err = store.Put(ctx, memory.Record{Scope: CandidateScope(project), Key: key, Kind: "procedure", Text: string(raw), Evidence: "Offline hypothesis from recorded eval results; unevaluated and not active"})
	return candidate, err
}

// DiscardCandidate removes an unevaluated hypothesis without touching active policy.
func DiscardCandidate(ctx context.Context, store *memory.Store, project, id string) error {
	if len(id) != 64 {
		return fmt.Errorf("candidate ID must be a SHA-256 hex digest")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return fmt.Errorf("invalid candidate ID: %w", err)
	}
	if err := store.Forget(ctx, CandidateScope(project), id); err != nil {
		return err
	}
	return store.Forget(ctx, PlanScope(project), id)
}

// ListCandidates returns the bounded queued candidates for inspection.
func ListCandidates(ctx context.Context, store *memory.Store, project string) ([]Candidate, error) {
	records, err := store.Search(ctx, CandidateScope(project), "", 20)
	if err != nil {
		return nil, err
	}
	out := make([]Candidate, 0, len(records))
	for _, r := range records {
		var c Candidate
		if err := json.Unmarshal([]byte(r.Text), &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}
