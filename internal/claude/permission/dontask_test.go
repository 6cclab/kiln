package permission

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/andrepato/harness/internal/claude/paths"
	"github.com/andrepato/harness/internal/claude/settings"
)

// recorder is a prompter that answers with kind and keeps every request
// it was shown.
type recorder struct {
	kind PromptKind
	seen []Request
}

func (r *recorder) prompt(_ context.Context, req Request) (PromptChoice, error) {
	r.seen = append(r.seen, req)
	return PromptChoice{Kind: r.kind}, nil
}

func checkOK(t *testing.T, g *Gate, req Request) bool {
	t.Helper()
	b, err := g.Check(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return b == nil
}

// TestGateDontAskSavesARulePerSubcommand: "don't ask again" on a compound
// line saves one rule for each command that needed approval (not the
// read-only git status), persists each through SaveRule, adds each to the
// gate as a local-settings rule, and a later command on its own is then
// approved without a prompt.
func TestGateDontAskSavesARulePerSubcommand(t *testing.T) {
	root := work(t)
	var saved []string
	g := NewGate(GateOptions{Mode: settings.ModeManual, Roots: []string{root},
		SaveRule: func(r string) { saved = append(saved, r) }})
	rec := &recorder{kind: PromptAllowAlways}
	g.SetPrompter(rec.prompt)

	if !checkOK(t, g, Request{ToolName: "bash", PrimaryArg: "git status && npm test && make build"}) {
		t.Fatal("allow-always should approve the line")
	}
	if len(rec.seen) != 1 {
		t.Fatalf("prompted %d times, want 1", len(rec.seen))
	}
	want := []string{"npm test *", "make build *"}
	if got := rec.seen[0]; !got.Grantable || !reflect.DeepEqual(got.DontAskRules, want) {
		t.Errorf("prompt request: Grantable=%v DontAskRules=%q, want true %q", got.Grantable, got.DontAskRules, want)
	}
	if wantSaved := []string{"Bash(npm test *)", "Bash(make build *)"}; !reflect.DeepEqual(saved, wantSaved) {
		t.Errorf("saved %q, want %q", saved, wantSaved)
	}
	p := g.Permissions()
	local := settings.RuleSource{Scope: paths.ScopeLocal, File: filepath.Join(root, ".claude", "settings.local.json")}
	for _, r := range []string{"Bash(npm test *)", "Bash(make build *)"} {
		found := false
		for i, a := range p.Allow {
			if a == r {
				found = true
				if i >= len(p.AllowFrom) || p.AllowFrom[i] != local {
					t.Errorf("%s: source %+v, want %+v", r, sourceAt(p.AllowFrom, i), local)
				}
			}
		}
		if !found {
			t.Errorf("%s not in the gate's allow rules %q", r, p.Allow)
		}
	}

	rec.kind = PromptDeny
	for _, cmd := range []string{"npm test", "npm test -- upload", "make build"} {
		if !checkOK(t, g, Request{ToolName: "bash", PrimaryArg: cmd}) || len(rec.seen) != 1 {
			t.Errorf("%q after the grant: prompted (%d prompts), want auto-approved", cmd, len(rec.seen))
		}
	}
	if checkOK(t, g, Request{ToolName: "bash", PrimaryArg: "npm test && rm -rf build"}) || len(rec.seen) != 2 {
		t.Errorf("a line with a command no rule names must still ask; prompts=%d", len(rec.seen))
	}
}

// TestGateGrantable: the prompt is told "don't ask again" is available
// only when the gate would honour it, and a PromptAllowAlways answer to a
// prompt without it approves once and grants nothing.
func TestGateGrantable(t *testing.T) {
	root := work(t)
	ledger := filepath.Join(root, ".harness", "plans", "ledger.md")
	cases := []struct {
		name      string
		opts      GateOptions
		req       Request
		grantable bool
	}{
		{name: "bash, no rules",
			opts: GateOptions{Mode: settings.ModeManual},
			req:  Request{ToolName: "bash", PrimaryArg: "npm test"}, grantable: true},
		{name: "bash, ask rule hit",
			opts: GateOptions{Mode: settings.ModeManual, Permissions: settings.Permissions{Ask: []string{"Bash(npm test *)"}}},
			req:  Request{ToolName: "bash", PrimaryArg: "npm test"}},
		{name: "bash, ask rule hit on one command of a line",
			opts: GateOptions{Mode: settings.ModeManual, Permissions: settings.Permissions{Ask: []string{"Bash(make *)"}}},
			req:  Request{ToolName: "bash", PrimaryArg: "npm test && make build"}},
		{name: "bash, unknown operand while a path rule denies",
			opts: GateOptions{Mode: settings.ModeManual, Permissions: settings.Permissions{Deny: []string{"Read(./secret/**)"}}},
			req:  Request{ToolName: "bash", PrimaryArg: "npm test $F"}},
		{name: "bash, unparseable",
			opts: GateOptions{Mode: settings.ModeManual},
			req:  Request{ToolName: "bash", PrimaryArg: "npm test &&"}},
		{name: "bash, more than five rules needed",
			opts: GateOptions{Mode: settings.ModeManual},
			req:  Request{ToolName: "bash", PrimaryArg: "a1 x && a2 x && a3 x && a4 x && a5 x && a6 x"}},
		{name: "bash, exactly five rules",
			opts: GateOptions{Mode: settings.ModeManual},
			req:  Request{ToolName: "bash", PrimaryArg: "a1 x && a2 x && a3 x && a4 x && a5 x"}, grantable: true},
		{name: "bash in plan mode",
			opts: GateOptions{Mode: settings.ModePlan},
			req:  Request{ToolName: "bash", PrimaryArg: "npm test"}, grantable: true},
		{name: "generic tool, no rules",
			opts: GateOptions{Mode: settings.ModeManual},
			req:  Request{ToolName: "web_fetch", PrimaryArg: "https://example.com"}, grantable: true},
		{name: "generic tool, ask rule hit",
			opts: GateOptions{Mode: settings.ModeManual, Permissions: settings.Permissions{Ask: []string{"WebFetch"}}},
			req:  Request{ToolName: "web_fetch", PrimaryArg: "https://example.com"}},
		{name: "plan-mode edit of the ledger an ask rule names",
			opts: GateOptions{Mode: settings.ModePlan, PlanLedgerPath: ledger,
				Permissions: settings.Permissions{Ask: []string{"Edit(.harness/plans/ledger.md)"}}},
			req: Request{ToolName: "write", PrimaryArg: ledger, Args: map[string]any{"path": ledger}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var saved []string
			c.opts.Roots = []string{root}
			c.opts.SaveRule = func(r string) { saved = append(saved, r) }
			g := NewGate(c.opts)
			rec := &recorder{kind: PromptAllowAlways}
			g.SetPrompter(rec.prompt)
			if !checkOK(t, g, c.req) {
				t.Fatal("an approved prompt should allow the call")
			}
			if len(rec.seen) != 1 {
				t.Fatalf("prompted %d times, want 1", len(rec.seen))
			}
			if got := rec.seen[0].Grantable; got != c.grantable {
				t.Errorf("Grantable = %v, want %v (rules %q)", got, c.grantable, rec.seen[0].DontAskRules)
			}
			// The same call again: a grant the prompt offered is honoured;
			// an answer to a prompt that did not offer it granted nothing.
			checkOK(t, g, c.req)
			wantPrompts := 2
			if c.grantable {
				wantPrompts = 1
			}
			if len(rec.seen) != wantPrompts {
				t.Errorf("after allow-always, the same call prompted %d times in all, want %d", len(rec.seen), wantPrompts)
			}
			if !c.grantable && (len(saved) > 0 || len(g.SessionGrants()) > 0) {
				t.Errorf("not grantable, yet saved %q and granted %q", saved, g.SessionGrants())
			}
		})
	}
}
