package permission

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/andrepato/harness/internal/claude/settings"
)

// TestPlanLedger_AskRuleStillAsks: an ask rule naming the ledger reaches
// the prompter; the carve-out applies only when no deny or ask rule
// matched.
func TestPlanLedger_AskRuleStillAsks(t *testing.T) {
	root := work(t)
	ledger := filepath.Join(root, ".harness", "plans", "ledger.md")
	g := NewGate(GateOptions{
		Mode:           settings.ModePlan,
		Roots:          []string{root},
		PlanLedgerPath: ledger,
		Permissions:    settings.Permissions{Ask: []string{"Edit(.harness/plans/ledger.md)"}},
	})
	asked := 0
	g.SetPrompter(func(ctx context.Context, req Request) (PromptChoice, error) {
		asked++
		return PromptChoice{Kind: PromptAllow}, nil
	})
	blocked, err := g.Check(context.Background(), Request{ToolName: "write", PrimaryArg: ledger, Args: map[string]any{"path": ledger}})
	if err != nil {
		t.Fatal(err)
	}
	if asked != 1 || blocked != nil {
		t.Errorf("ask rule on the ledger: asked=%d blocked=%v, want one prompt, then allowed", asked, blocked)
	}
}

// TestPlanLedger_ToolPathSpellings: the call's path is resolved the way
// the tools resolve it, so "@path" and "file://" spellings of the ledger
// are the ledger.
func TestPlanLedger_ToolPathSpellings(t *testing.T) {
	root := work(t)
	ledger := filepath.Join(root, ".harness", "plans", "ledger.md")
	g := NewGate(GateOptions{Mode: settings.ModePlan, Roots: []string{root}, PlanLedgerPath: ledger})
	for _, p := range []string{"@.harness/plans/ledger.md", "file://" + ledger} {
		blocked, err := g.Check(context.Background(), Request{ToolName: "write", PrimaryArg: p, Args: map[string]any{"path": p}})
		if err != nil {
			t.Fatal(err)
		}
		if blocked != nil {
			t.Errorf("write to %q refused: %s", p, blocked.Reason)
		}
	}
}
