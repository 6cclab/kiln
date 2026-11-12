package learning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/memory"
)

// ObservationConfig bounds opt-in local outcome capture and suggestion review.
type ObservationConfig struct {
	Enabled                 bool              `json:"enabled"`
	FactMemoryEnabled       bool              `json:"-"`
	FactReadAllowed         func(string) bool `json:"-"`
	MaxOutcomes             int               `json:"maxOutcomes"`
	MinSamples              int               `json:"minSamples"`
	SourceSkillBudgetTokens int               `json:"-"`
}

// NormalizeObservationConfig applies local defaults and enforces hard bounds.
func NormalizeObservationConfig(c ObservationConfig) (ObservationConfig, error) {
	if !c.Enabled {
		return c, nil
	}
	if c.MaxOutcomes == 0 {
		c.MaxOutcomes = 100
	}
	if c.MinSamples == 0 {
		c.MinSamples = 5
	}
	if c.MaxOutcomes < 1 || c.MaxOutcomes > 1000 || c.MinSamples < 2 || c.MinSamples > 100 || c.MinSamples > c.MaxOutcomes || c.SourceSkillBudgetTokens < 0 || c.SourceSkillBudgetTokens > 32768 {
		return c, fmt.Errorf("learning automation limits must be maxOutcomes 1..1000 and minSamples 2..100 within retention")
	}
	return c, nil
}

// Outcome is a sanitized aggregate for one main-lane operation. Hashes are
// identities, not contents; no prompt, argument, command, path or error text is stored.
type Outcome struct {
	ID                string `json:"id"`
	ObservedAt        int64  `json:"observedAt"`
	Status            string `json:"status"`
	ModelHash         string `json:"modelHash"`
	ModelsUsed        int    `json:"modelsUsed"`
	BinaryHash        string `json:"binaryHash"`
	UsageScope        string `json:"usageScope"`
	InputTokens       int    `json:"inputTokens"`
	OutputTokens      int    `json:"outputTokens"`
	CacheReadTokens   int    `json:"cacheReadTokens"`
	CacheWriteTokens  int    `json:"cacheWriteTokens"`
	DurationMS        int64  `json:"durationMs"`
	ToolSuccess       int    `json:"toolSuccess"`
	ToolFailure       int    `json:"toolFailure"`
	BashFailure       int    `json:"bashFailure"`
	ReadFailure       int    `json:"readFailure"`
	WriteFailure      int    `json:"writeFailure"`
	SearchFailure     int    `json:"searchFailure"`
	TaskFailure       int    `json:"taskFailure"`
	OtherFailure      int    `json:"otherFailure"`
	VerifiedCommands  int    `json:"verifiedCommands"`
	SkillBudgetTokens int    `json:"skillBudgetTokens,omitempty"`
	// MutationChecks counts check commands that also wrote files (a change
	// made to see a test catch it, then restored). Their failures are
	// intended, so they count as neither tool success nor failure.
	MutationChecks int `json:"mutationChecks,omitempty"`
	// Shape is what the run did (RunShape*); "" on records saved before it
	// existed, which are left out of shape statistics.
	Shape string `json:"shape,omitempty"`
}

// Run shapes, read from what a run did rather than how it was asked: it
// changed no files, changed files and ran a check, or changed files and ran
// none. Whether a check passed, or was the right one, is a different question.
const (
	RunShapeQuestion             = "question"
	RunShapeCodeChange           = "code_change"
	RunShapeCodeChangeUnverified = "code_change_unverified"
)

// ClassifyRunShape returns the shape of a run that made fileEdits file
// changes and checks check runs (mutation checks included).
func ClassifyRunShape(fileEdits, checks int) string {
	switch {
	case fileEdits == 0:
		return RunShapeQuestion
	case checks > 0:
		return RunShapeCodeChange
	default:
		return RunShapeCodeChangeUnverified
	}
}

// OutcomeScope isolates run aggregates from conversational memory and policy.
func OutcomeScope(project string) string   { return "outcome:" + project }
func dismissalScope(project string) string { return "outcome-dismissed:" + project }

// HashIdentity creates a fixed-length digest of a runtime identifier.
func HashIdentity(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (o Outcome) validate() error {
	if !validDigest(o.ID) || !validDigest(o.ModelHash) || !validDigest(o.BinaryHash) || (o.Status != "completed" && o.Status != "failed") || o.ObservedAt <= 0 || o.ObservedAt > time.Now().Add(time.Hour).Unix() {
		return fmt.Errorf("invalid outcome identity or status")
	}
	for _, n := range []int{o.InputTokens, o.OutputTokens, o.CacheReadTokens, o.CacheWriteTokens, o.ToolSuccess, o.ToolFailure, o.BashFailure, o.ReadFailure, o.WriteFailure, o.SearchFailure, o.TaskFailure, o.OtherFailure, o.VerifiedCommands, o.SkillBudgetTokens, o.MutationChecks} {
		if n < 0 || n > 1_000_000_000 {
			return fmt.Errorf("outcome count outside bounds")
		}
	}
	switch o.Shape {
	case "", RunShapeQuestion, RunShapeCodeChange, RunShapeCodeChangeUnverified:
	default:
		return fmt.Errorf("invalid outcome run shape")
	}
	if o.DurationMS < 0 || o.DurationMS > 7*24*60*60*1000 || o.SkillBudgetTokens > 32768 || o.BashFailure+o.ReadFailure+o.WriteFailure+o.SearchFailure+o.TaskFailure+o.OtherFailure != o.ToolFailure || o.VerifiedCommands > o.ToolSuccess || o.ModelsUsed < 0 || o.ModelsUsed > 100 || (o.ModelsUsed == 0 && o.ModelHash != HashIdentity("")) || o.UsageScope != "main-lane model responses; worker and side usage excluded" {
		return fmt.Errorf("inconsistent outcome counters")
	}
	return nil
}

// RecordOutcome persists one sanitized operation once, then enforces retention.
func RecordOutcome(ctx context.Context, store *memory.Store, project string, o Outcome, maxOutcomes int) error {
	if err := o.validate(); err != nil {
		return err
	}
	if maxOutcomes < 1 || maxOutcomes > 1000 {
		return fmt.Errorf("invalid outcome retention limit")
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return err
	}
	_, err = store.PutUniquePruned(ctx, memory.Record{Scope: OutcomeScope(project), Key: o.ID, Kind: "observation", Text: string(raw), Evidence: "Sanitized local runtime counters; no semantic correctness claim"}, maxOutcomes)
	return err
}

// ListOutcomes returns retained aggregates newest first.
func ListOutcomes(ctx context.Context, store *memory.Store, project string) ([]Outcome, error) {
	records, err := store.Recent(ctx, OutcomeScope(project), 1000)
	if err != nil {
		return nil, err
	}
	out := make([]Outcome, 0, len(records))
	for i := range records {
		var o Outcome
		if err := json.Unmarshal([]byte(records[i].Text), &o); err != nil {
			return nil, err
		}
		if err := o.validate(); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

// Suggestion is a reviewable hypothesis derived only from aggregate counts.
type Suggestion struct {
	Kind         string `json:"kind"`
	Summary      string `json:"summary"`
	Evidence     string `json:"evidence"`
	Samples      int    `json:"samples"`
	AffectedRuns int    `json:"affectedRuns"`
}

var suggestionKinds = map[string]bool{"tool-failures": true, "run-failures": true, "high-input": true, "unverified-changes": true}

// Suggestions computes deterministic hypotheses without changing policy.
func Suggestions(outcomes []Outcome, minSamples int) []Suggestion {
	if minSamples < 2 || len(outcomes) < minSamples {
		return []Suggestion{}
	}
	var failedRuns, toolFailureRuns, toolFailures, highInputRuns, changedRuns, uncheckedRuns int
	for _, o := range outcomes {
		if o.Status == "failed" {
			failedRuns++
		}
		if o.ToolFailure > 0 {
			toolFailureRuns++
			toolFailures += o.ToolFailure
		}
		if o.InputTokens+o.CacheReadTokens+o.CacheWriteTokens >= 32000 {
			highInputRuns++
		}
		switch o.Shape {
		case RunShapeCodeChange:
			changedRuns++
		case RunShapeCodeChangeUnverified:
			changedRuns++
			uncheckedRuns++
		}
	}
	out := make([]Suggestion, 0, 4)
	if toolFailureRuns >= 2 && toolFailures >= 3 {
		out = append(out, Suggestion{Kind: "tool-failures", Summary: "Review recurring tool failures", Evidence: fmt.Sprintf("%d failed tool calls across %d of %d observed runs; cause and correctness are unknown.", toolFailures, toolFailureRuns, len(outcomes)), Samples: len(outcomes), AffectedRuns: toolFailureRuns})
	}
	if failedRuns >= 3 {
		out = append(out, Suggestion{Kind: "run-failures", Summary: "Investigate failed runs", Evidence: fmt.Sprintf("%d of %d observed runs failed; cause is unknown.", failedRuns, len(outcomes)), Samples: len(outcomes), AffectedRuns: failedRuns})
	}
	if highInputRuns >= minSamples {
		out = append(out, Suggestion{Kind: "high-input", Summary: "Investigate context use", Evidence: fmt.Sprintf("%d of %d observed runs used at least 32000 main-lane input and cache tokens; no improvement is established.", highInputRuns, len(outcomes)), Samples: len(outcomes), AffectedRuns: highInputRuns})
	}
	if changedRuns >= minSamples && uncheckedRuns >= 3 && uncheckedRuns*2 >= changedRuns {
		out = append(out, Suggestion{Kind: "unverified-changes", Summary: "Review changes made without a check", Evidence: fmt.Sprintf("%d of %d observed runs that changed files ran no recognised test, build or lint command; whether a check was needed is unknown.", uncheckedRuns, changedRuns), Samples: changedRuns, AffectedRuns: uncheckedRuns})
	}
	return out
}

// ReviewSuggestions omits categories explicitly dismissed by the user.
func ReviewSuggestions(ctx context.Context, store *memory.Store, project string, minSamples int) ([]Suggestion, error) {
	outcomes, err := ListOutcomes(ctx, store, project)
	if err != nil {
		return nil, err
	}
	proposed := Suggestions(outcomes, minSamples)
	out := make([]Suggestion, 0, len(proposed))
	for _, s := range proposed {
		dismissed, err := store.Current(ctx, dismissalScope(project), s.Kind, "")
		if err != nil {
			return nil, err
		}
		if dismissed == nil {
			out = append(out, s)
		}
	}
	return out, nil
}

// DismissSuggestion hides one fixed category until outcomes are forgotten.
func DismissSuggestion(ctx context.Context, store *memory.Store, project, kind string) error {
	if !suggestionKinds[kind] || strings.ContainsAny(kind, "/\\") {
		return fmt.Errorf("unknown suggestion kind")
	}
	_, err := store.PutUniquePruned(ctx, memory.Record{Scope: dismissalScope(project), Key: kind, Kind: "procedure", Text: "dismissed", Evidence: "Explicit user command"}, 3)
	return err
}

// ForgetOutcomes removes all aggregate runs and dismissals in this project.
func ForgetOutcomes(ctx context.Context, store *memory.Store, project string) error {
	if err := store.ForgetScope(ctx, OutcomeScope(project)); err != nil {
		return err
	}
	if err := store.ForgetScope(ctx, dismissalScope(project)); err != nil {
		return err
	}
	if err := store.ForgetScope(ctx, ProposalScope(project)); err != nil {
		return err
	}
	if err := store.ForgetScope(ctx, proposalDismissalScope(project)); err != nil {
		return err
	}
	if err := store.ForgetScope(ctx, factScope(project)); err != nil {
		return err
	}
	return store.ForgetScope(ctx, factDismissalScope(project))
}
