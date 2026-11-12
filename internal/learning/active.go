package learning

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/andrepato/harness/internal/memory"
)

// Active is a promoted policy plus the evidence that authorized it.
type Active struct {
	Policy     Policy `json:"policy"`
	Report     Report `json:"report"`
	PromotedAt int64  `json:"promotedAt"`
}

// Scope keeps learned policy records out of conversational memory retrieval.
func Scope(project string) string { return "policy:" + project }

// Promote independently reevaluates the evidence before recording a policy.
func Promote(ctx context.Context, store *memory.Store, project string, policy Policy, baseline, candidate string) (Report, error) {
	report, err := EvaluateFiles(policy, baseline, candidate)
	if err != nil {
		return report, err
	}
	if !report.Accepted {
		return report, fmt.Errorf("policy rejected: %s", report.Reason)
	}
	return report, storePromoted(ctx, store, project, policy, report)
}

func storePromoted(ctx context.Context, store *memory.Store, project string, policy Policy, report Report) error {
	previous, err := Load(ctx, store, project)
	if err != nil {
		return err
	}
	if previous != nil && previous.Report.BinaryHash == report.BinaryHash && SameRoles(previous.Report.Roles, report.Roles) && report.BaselinePolicyHash != previous.Policy.Hash() {
		return fmt.Errorf("baseline does not match the current personal policy")
	}
	active := Active{Policy: policy, Report: report, PromotedAt: time.Now().Unix()}
	raw, err := json.Marshal(active)
	if err != nil {
		return err
	}
	_, err = store.Put(ctx, memory.Record{Scope: Scope(project), Key: "active", Kind: "procedure", Text: string(raw), Evidence: "Paired held-out evaluation passed; previous policies retained for rollback"})
	return err
}

// Load returns a promoted policy without changing it. Runtime callers must also
// match its binary hash, model and roles before applying it.
func Load(ctx context.Context, store *memory.Store, project string) (*Active, error) {
	records, err := store.Search(ctx, Scope(project), "active", 1)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	var active Active
	if err := json.Unmarshal([]byte(records[0].Text), &active); err != nil {
		return nil, err
	}
	if !active.Report.Accepted {
		return nil, fmt.Errorf("active policy lacks accepted evidence")
	}
	if err := active.Policy.Validate(); err != nil {
		return nil, err
	}
	return &active, nil
}

// SameRoles ensures a policy is not reused after its model-role mapping changes.
func SameRoles(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
