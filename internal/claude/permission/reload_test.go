package permission

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/andrepato/harness/internal/claude/paths"
	"github.com/andrepato/harness/internal/claude/settings"
)

// ReplaceSettingsRules swaps the rules that came from settings files for a
// fresh read, keeping command-line and session rules, as Claude Code's
// reload does.
func TestReplaceSettingsRules(t *testing.T) {
	root := t.TempDir()
	project := settings.RuleSource{Scope: paths.ScopeProject, File: filepath.Join(root, ".claude", "settings.json")}
	user := settings.RuleSource{Scope: paths.ScopeUser, File: "/home/u/.claude/settings.json", Root: "/home/u/.claude"}
	g := NewGate(GateOptions{
		Permissions: settings.Permissions{
			// The last allow rule and the deny rule come from the command
			// line: no source entry.
			Allow:     []string{"Bash(make *)", "Bash(npm test)"},
			AllowFrom: []settings.RuleSource{project},
			Deny:      []string{"Bash(rm *)", "Bash(git push *)"},
			DenyFrom:  []settings.RuleSource{user},
		},
		Mode:  settings.ModeManual,
		Roots: []string{root},
	})
	before := g.Permissions()

	g.ReplaceSettingsRules(settings.Permissions{
		Allow:     []string{"Bash(go test *)"},
		AllowFrom: []settings.RuleSource{project},
		Ask:       []string{"Bash(git commit *)"},
		AskFrom:   []settings.RuleSource{project},
	})
	p := g.Permissions()
	decide := func(cmd string) settings.Decision {
		return settings.DecideIn(p, root, "bash", cmd, settings.ModeManual)
	}
	for cmd, want := range map[string]settings.Decision{
		"go test ./...":     settings.Allow, // new file rule
		"npm test":          settings.Allow, // command-line rule kept
		"make build":        settings.Ask,   // removed from its file
		"rm -rf x":          settings.Ask,   // user deny removed from its file
		"git push origin x": settings.Deny,  // command-line deny kept
		"git commit -m x":   settings.Ask,   // new ask rule
	} {
		if got := decide(cmd); got != want {
			t.Errorf("%s: %v, want %v (rules %+v)", cmd, got, want, p)
		}
	}
	if len(p.Allow) != len(p.AllowFrom) || len(p.Deny) != len(p.DenyFrom) || len(p.Ask) != len(p.AskFrom) {
		t.Errorf("sources not aligned: %+v", p)
	}
	// A decision already holding the old lists keeps reading them whole.
	if len(before.Allow) != 2 || before.Allow[0] != "Bash(make *)" || len(before.Deny) != 2 {
		t.Errorf("the old snapshot changed: %+v", before)
	}

	// The gate itself decides by the new rules.
	pr := &promptRecorder{kind: PromptDeny}
	g.SetPrompter(pr.prompt)
	if blocked, _, err := g.CheckWithOutcome(context.Background(), bashReq("go test ./...")); err != nil || blocked != nil || len(pr.reqs) != 0 {
		t.Errorf("go test after reload: blocked=%+v prompts=%d err=%v, want allowed", blocked, len(pr.reqs), err)
	}
}

// A "don't ask again" rule kiln saved lives in .kiln/settings.local.json:
// a reload takes it from there, and does not keep a second copy.
func TestReplaceSettingsRulesTakesSavedRulesFromTheirFile(t *testing.T) {
	root := t.TempDir()
	g := NewGate(GateOptions{Mode: settings.ModeManual, Roots: []string{root}})
	g.AddRule(RuleAllow, "Bash(go vet *)")
	local := settings.RuleSource{Scope: paths.ScopeLocal, File: paths.KilnLocalSettingsPath(root)}
	g.ReplaceSettingsRules(settings.Permissions{Allow: []string{"Bash(go vet *)"}, AllowFrom: []settings.RuleSource{local}})
	if p := g.Permissions(); len(p.Allow) != 1 {
		t.Errorf("allow = %v, want the one rule from its file", p.Allow)
	}
}
