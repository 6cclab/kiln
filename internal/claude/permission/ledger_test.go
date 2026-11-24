package permission

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// TestPlanLedger_DenyRuleWins: Claude Code treats a deny rule as absolute
// in every mode, so a deny rule covering the ledger refuses the write the
// plan-mode carve-out would otherwise auto-approve.
func TestPlanLedger_DenyRuleWins(t *testing.T) {
	root := work(t)
	ledger := filepath.Join(root, ".harness", "plans", "ledger.md")
	g := NewGate(GateOptions{
		Mode:           settings.ModePlan,
		Roots:          []string{root},
		PlanLedgerPath: ledger,
		Permissions:    settings.Permissions{Deny: []string{"Write"}},
	})
	blocked, outcome, err := g.CheckWithOutcome(context.Background(), Request{ToolName: "write", PrimaryArg: ledger, Args: map[string]any{"path": ledger}})
	if err != nil {
		t.Fatal(err)
	}
	if blocked == nil || outcome == OutcomeAuto {
		t.Fatalf("Deny(Write) on the ledger: blocked=%v outcome=%v, want refused", blocked, outcome)
	}
}

// TestPlanLedger_DenyRulePathSpellings: a deny rule naming the ledger
// refuses the carve-out however the rule and the call spell the path
// (Claude Code's anchors: "//abs", "~/p", "/p" under the settings source,
// "p" and "./p" under the cwd; symlinks resolved). The control loop shows
// every call form takes the carve-out when no rule matches, so each
// refusal is the rule's doing.
func TestPlanLedger_DenyRulePathSpellings(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "proj")
	ledgerRel := filepath.Join(".harness", "plans", "ledger.md")
	ledgerAbs := filepath.Join(root, ledgerRel)
	if err := os.MkdirAll(filepath.Dir(ledgerAbs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ledgerAbs, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// link -> proj: a ledger configured through a symlinked directory,
	// denied by a rule naming the real location.
	link := filepath.Join(home, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	ledgerViaLink := filepath.Join(link, ledgerRel)
	realLedger, err := filepath.EvalSymlinks(ledgerAbs)
	if err != nil {
		t.Fatal(err)
	}

	sep := string(filepath.Separator)
	cases := []struct {
		name     string
		ledger   string // GateOptions.PlanLedgerPath; ledgerAbs when ""
		denyRule string
		callArg  string
	}{
		{"relative call vs absolute (//) rule", "", "Write(/" + ledgerAbs + ")", ledgerRel},
		{"./ call vs absolute rule", "", "Write(/" + ledgerAbs + ")", "./" + ledgerRel},
		{"// call vs absolute rule", "", "Write(/" + ledgerAbs + ")", strings.Replace(ledgerAbs, sep, sep+sep, 1)},
		// Concatenated, not filepath.Join, which would clean the ".." away.
		{".. call vs absolute rule", "", "Write(/" + ledgerAbs + ")", root + "/../" + filepath.Base(root) + "/" + filepath.ToSlash(ledgerRel)},
		{"~ call vs absolute rule", "", "Edit(/" + ledgerAbs + ")", "~/proj/" + filepath.ToSlash(ledgerRel)},
		{"absolute call vs relative rule", "", "Write(" + ledgerRel + ")", ledgerAbs},
		{"absolute call vs ./ rule", "", "Edit(./" + filepath.ToSlash(ledgerRel) + ")", ledgerAbs},
		{"absolute call vs /p rule", "", "Edit(/" + filepath.ToSlash(ledgerRel) + ")", ledgerAbs},
		{"absolute call vs ~ rule", "", "Edit(~/proj/" + filepath.ToSlash(ledgerRel) + ")", ledgerAbs},
		{"absolute call vs any-depth glob rule", "", "Edit(ledger.md)", ledgerAbs},
		{"symlinked call vs rule on the real path", ledgerViaLink, "Edit(/" + realLedger + ")", ledgerViaLink},
	}
	gate := func(ledger string, perms settings.Permissions) *Gate {
		if ledger == "" {
			ledger = ledgerAbs
		}
		return NewGate(GateOptions{Mode: settings.ModePlan, Roots: []string{root}, PlanLedgerPath: ledger, Permissions: perms})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := Request{ToolName: "write", PrimaryArg: c.callArg, Args: map[string]any{"path": c.callArg}}
			blocked, outcome, err := gate(c.ledger, settings.Permissions{Deny: []string{c.denyRule}}).CheckWithOutcome(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if blocked == nil || outcome == OutcomeAuto {
				t.Errorf("rule %q, call %q: blocked=%v outcome=%v, want refused", c.denyRule, c.callArg, blocked, outcome)
			}
			blocked, outcome, err = gate(c.ledger, settings.Permissions{}).CheckWithOutcome(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if blocked != nil || outcome != OutcomeAuto {
				t.Errorf("control, call %q with no rules: blocked=%v outcome=%v, want the carve-out (auto)", c.callArg, blocked, outcome)
			}
		})
	}
}
