package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	settings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/diag"
	"github.com/andrepato/harness/internal/learning"
	"github.com/andrepato/harness/internal/memory"
)

// Learning manages reversible per-project policies without starting model calls.
func Learning(ctx context.Context, args Args, out, stderr io.Writer) int {
	fail := func(err error) int { writeDiagnostic(stderr, err); return 1 }
	usage := func() int {
		writeDiagnostic(stderr, "usage: kiln learning outcomes | suggestions | dismiss <kind> | proposals | dismiss-proposal <id> | fact-candidates | dismiss-fact <id> | forget-outcomes | propose [skill-budget] | queue <source-policy.json> <results.jsonl> [max-estimated-usd] | candidates | plan <candidate-id> <scenario-dir> [fixtures-dir] | discard <candidate-id> | promote-candidate <candidate-id> <baseline.jsonl> <candidate.jsonl> | promote <policy> <baseline.jsonl> <candidate.jsonl> | status | history | rollback <id> | forget")
		return 2
	}
	if len(args.Positional) == 0 {
		return usage()
	}
	action := args.Positional[0]
	if action == "propose" {
		if len(args.Positional) > 2 {
			return usage()
		}
		budget := 512
		if len(args.Positional) == 2 {
			n, err := strconv.Atoi(args.Positional[1])
			if err != nil {
				return fail(err)
			}
			budget = n
		}
		p := learning.Policy{SkillBudgetTokens: budget, ConservativeRouting: true}
		if err := p.Validate(); err != nil {
			return fail(err)
		}
		if err := json.NewEncoder(out).Encode(p); err != nil {
			return fail(err)
		}
		return 0
	}
	if ((action == "queue" || action == "plan") && (len(args.Positional) < 3 || len(args.Positional) > 4)) || ((action == "promote" || action == "promote-candidate") && len(args.Positional) != 4) || ((action == "rollback" || action == "discard" || action == "dismiss" || action == "dismiss-proposal" || action == "dismiss-fact") && len(args.Positional) != 2) || ((action == "status" || action == "history" || action == "forget" || action == "candidates" || action == "outcomes" || action == "suggestions" || action == "proposals" || action == "fact-candidates" || action == "forget-outcomes") && len(args.Positional) != 1) {
		return usage()
	}
	if action != "queue" && action != "candidates" && action != "plan" && action != "discard" && action != "promote-candidate" && action != "promote" && action != "rollback" && action != "status" && action != "history" && action != "forget" && action != "outcomes" && action != "suggestions" && action != "dismiss" && action != "proposals" && action != "dismiss-proposal" && action != "fact-candidates" && action != "dismiss-fact" && action != "forget-outcomes" {
		return usage()
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fail(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fail(err)
	}
	path := filepath.Join(home, ".kiln", "memory.db")
	if _, err := os.Stat(path); os.IsNotExist(err) && (action == "status" || action == "history" || action == "candidates" || action == "outcomes" || action == "suggestions" || action == "proposals" || action == "fact-candidates" || action == "forget-outcomes") {
		value := "null"
		if action == "candidates" || action == "outcomes" || action == "suggestions" || action == "proposals" || action == "fact-candidates" {
			value = "[]"
		}
		if _, err := fmt.Fprintln(out, value); err != nil {
			return fail(err)
		}
		return 0
	}
	store, err := memory.Open(path)
	if err != nil {
		return fail(err)
	}
	defer diag.Close(store)
	project := memoryScope(cwd)
	switch action {
	case "fact-candidates":
		candidates, err := learning.ListFactCandidates(ctx, store, project, memoryProjectRoot(cwd))
		if err != nil {
			return fail(err)
		}
		if err := json.NewEncoder(out).Encode(candidates); err != nil {
			return fail(err)
		}
	case "dismiss-fact":
		if err := learning.DismissFactCandidate(ctx, store, project, args.Positional[1]); err != nil {
			return fail(err)
		}
	case "outcomes":
		outcomes, err := learning.ListOutcomes(ctx, store, project)
		if err != nil {
			return fail(err)
		}
		if err := json.NewEncoder(out).Encode(outcomes); err != nil {
			return fail(err)
		}
	case "suggestions":
		opts := settings.LoadOptions{Sources: settingsSources(args.SettingSources), Extra: args.Settings, Trusted: folderTrusted(cwd), Headless: true}
		merged := settings.LoadSettings(cwd, opts)
		minSamples := settings.PositiveOr(merged.LearningAutomation.MinSamples, 5)
		suggestions, err := learning.ReviewSuggestions(ctx, store, project, minSamples)
		if err != nil {
			return fail(err)
		}
		if err := json.NewEncoder(out).Encode(suggestions); err != nil {
			return fail(err)
		}
	case "dismiss":
		if err := learning.DismissSuggestion(ctx, store, project, args.Positional[1]); err != nil {
			return fail(err)
		}
	case "proposals":
		proposals, err := learning.ListProposals(ctx, store, project)
		if err != nil {
			return fail(err)
		}
		if err := json.NewEncoder(out).Encode(proposals); err != nil {
			return fail(err)
		}
	case "dismiss-proposal":
		if err := learning.DismissProposal(ctx, store, project, args.Positional[1]); err != nil {
			return fail(err)
		}
	case "forget-outcomes":
		if err := learning.ForgetOutcomes(ctx, store, project); err != nil {
			return fail(err)
		}
	case "queue":
		p, err := learning.ReadPolicy(args.Positional[1])
		if err != nil {
			return fail(err)
		}
		maxCost := 1.0
		if len(args.Positional) == 4 {
			maxCost, err = strconv.ParseFloat(args.Positional[3], 64)
			if err != nil {
				return fail(err)
			}
		}
		candidate, err := learning.QueueFromResults(ctx, store, project, args.Positional[2], p, maxCost)
		if err != nil {
			return fail(err)
		}
		if err := json.NewEncoder(out).Encode(candidate); err != nil {
			return fail(err)
		}
	case "candidates":
		candidates, err := learning.ListCandidates(ctx, store, project)
		if err != nil {
			return fail(err)
		}
		if err := json.NewEncoder(out).Encode(candidates); err != nil {
			return fail(err)
		}
	case "plan":
		fixtures := filepath.Join(filepath.Dir(args.Positional[2]), "fixtures")
		if len(args.Positional) == 4 {
			fixtures = args.Positional[3]
		}
		manifest, err := learning.PlanCandidate(ctx, store, project, args.Positional[1], args.Positional[2], fixtures)
		if err != nil {
			return fail(err)
		}
		if err := json.NewEncoder(out).Encode(manifest); err != nil {
			return fail(err)
		}
	case "discard":
		if err := learning.DiscardCandidate(ctx, store, project, args.Positional[1]); err != nil {
			return fail(err)
		}
	case "promote":
		p, err := learning.ReadPolicy(args.Positional[1])
		if err != nil {
			return fail(err)
		}
		report, err := learning.Promote(ctx, store, project, p, args.Positional[2], args.Positional[3])
		if encodeErr := json.NewEncoder(out).Encode(report); encodeErr != nil {
			return fail(encodeErr)
		}
		if err != nil {
			return fail(err)
		}
	case "promote-candidate":
		report, err := learning.PromoteCandidate(ctx, store, project, args.Positional[1], args.Positional[2], args.Positional[3])
		if encodeErr := json.NewEncoder(out).Encode(report); encodeErr != nil {
			return fail(encodeErr)
		}
		if err != nil {
			return fail(err)
		}
	case "status":
		active, err := learning.Load(ctx, store, project)
		if err != nil {
			return fail(err)
		}
		if err := json.NewEncoder(out).Encode(active); err != nil {
			return fail(err)
		}
	case "history":
		raw, err := store.Export(ctx, learning.Scope(project))
		if err != nil {
			return fail(err)
		}
		if _, err := fmt.Fprintln(out, string(raw)); err != nil {
			return fail(err)
		}
	case "rollback":
		id, err := strconv.ParseInt(args.Positional[1], 10, 64)
		if err != nil {
			return fail(err)
		}
		if _, err := store.Restore(ctx, learning.Scope(project), id); err != nil {
			return fail(err)
		}
	case "forget":
		if err := store.Forget(ctx, learning.Scope(project), "active"); err != nil {
			return fail(err)
		}
	}
	return 0
}

// compatiblePolicy does no work unless a policy database already exists. The
// exact binary and role mapping must match the held-out evaluation evidence.
func compatiblePolicy(ctx context.Context, cwd string, roles map[string]string) (*learning.Active, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(home, ".kiln", "memory.db")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}
	store, err := memory.Open(path)
	if err != nil {
		return nil, err
	}
	defer diag.Close(store)
	active, err := learning.Load(ctx, store, memoryScope(cwd))
	if err != nil || active == nil {
		return active, err
	}
	if !learning.SameRoles(active.Report.Roles, roles) {
		return nil, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	hash, err := learning.FileHash(executable)
	if err != nil {
		return nil, err
	}
	if hash != active.Report.BinaryHash {
		return nil, nil
	}
	return active, nil
}

func policyCoversModel(active *learning.Active, model string) bool {
	for _, m := range active.Report.Models {
		if m == model {
			return true
		}
	}
	return false
}

func applyLearnedRouting(config *settings.ModelRouterConfig, active *learning.Active) {
	if active != nil && !config.ConservativeSet && len(config.Roles) == 0 && learning.SameRoles(config.ModelRoles, active.Report.Roles) && policyCoversModel(active, "kiln/auto") {
		config.Conservative = active.Policy.ConservativeRouting
		config.ConservativeSource = "evaluated personal policy"
	}
}
