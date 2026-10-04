package hooks

import (
	"testing"
)

// A PreToolUse hook's permissionDecision reaches the gate (Claude Code's
// hooks reference, "PreToolUse decision control"): "allow" and "ask" are
// passed on, several hooks combine deny > ask > allow, the deprecated
// top-level "approve"/"block" map to allow/deny, and allow/ask need the
// hook to name its event, as Claude Code requires.
func TestPreToolUseDecisions(t *testing.T) {
	dir := t.TempDir()
	hook := func(name, json string) string {
		return writeScript(t, dir, name, "cat >/dev/null\nprintf '%s' '"+json+"'")
	}
	chain := func(paths ...string) Config {
		var cmds []Command
		for _, p := range paths {
			cmds = append(cmds, Command{Type: "command", Command: p})
		}
		return Config{PreToolUse: []Matcher{{MatcherPattern: "Bash", Hooks: cmds}}}
	}
	allow := hook("allow.sh", `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","permissionDecisionReason":"RTK auto-rewrite","updatedInput":{"command":"rtk git status"}}}`)
	ask := hook("ask.sh", `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":"check it"}}`)
	deny := hook("deny.sh", `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"no"}}`)
	unnamed := hook("unnamed.sh", `{"hookSpecificOutput":{"permissionDecision":"allow"}}`)
	approve := hook("approve.sh", `{"decision":"approve","reason":"legacy ok"}`)
	block := hook("block.sh", `{"decision":"block","reason":"legacy no"}`)
	deferred := hook("defer.sh", `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"defer"}}`)
	bogus := hook("bogus.sh", `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"yes"}}`)

	cases := []struct {
		name     string
		cfg      Config
		decision Decision
		reason   string
		blocked  string
	}{
		{"allow", chain(allow), DecisionAllow, "RTK auto-rewrite", ""},
		{"ask", chain(ask), DecisionAsk, "check it", ""},
		{"ask beats allow, either order", chain(allow, ask), DecisionAsk, "check it", ""},
		{"ask beats allow, reversed", chain(ask, allow), DecisionAsk, "check it", ""},
		{"deny beats allow", chain(allow, deny), "", "", "no"},
		{"allow without hookEventName is ignored", chain(unnamed), "", "", ""},
		{"legacy approve is allow", chain(approve), DecisionAllow, "legacy ok", ""},
		{"legacy block is deny", chain(block), "", "", "legacy no"},
		{"defer is no decision", chain(deferred), "", "", ""},
		{"an unknown value is no decision", chain(bogus), "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := run(c.cfg, nil, dir)
			if out.Decision != c.decision || out.DecisionReason != c.reason {
				t.Errorf("decision=%q reason=%q, want %q %q", out.Decision, out.DecisionReason, c.decision, c.reason)
			}
			got := ""
			if out.Blocked != nil {
				got = out.Blocked.Reason
			}
			if got != c.blocked {
				t.Errorf("blocked=%q, want %q", got, c.blocked)
			}
		})
	}

	t.Run("an allow's rewrite is still applied", func(t *testing.T) {
		out := run(chain(allow), nil, dir)
		if out.UpdatedInput["command"] != "rtk git status" {
			t.Errorf("updatedInput = %v", out.UpdatedInput)
		}
	})

	t.Run("only PreToolUse reads a decision", func(t *testing.T) {
		out := RunHooks(RunOptions{
			Config:      Config{PostToolUse: []Matcher{{MatcherPattern: "Bash", Hooks: []Command{{Type: "command", Command: block}}}}},
			Event:       PostToolUse,
			ToolName:    "bash",
			HasToolName: true,
			Payload:     Payload{Cwd: dir},
		})
		if out.Blocked != nil || out.Decision != "" {
			t.Errorf("PostToolUse decision:block read as a permission decision: %+v", out)
		}
	})

	t.Run("GuardToolCall hands the gate the rewritten input and the decision", func(t *testing.T) {
		var gotArg string
		var gotDecision Decision
		var gotReason string
		_, err := GuardToolCall(GuardOptions{
			Config:   chain(allow),
			ToolName: "bash",
			Args:     map[string]any{"command": "git status"},
			Cwd:      dir,
			PrimaryArgOf: func(args map[string]any) (string, bool) {
				s, ok := args["command"].(string)
				return s, ok
			},
			Check: func(_, primary string, _ bool, _ map[string]any, d Decision, reason string) (*Blocked, error) {
				gotArg, gotDecision, gotReason = primary, d, reason
				return nil, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if gotArg != "rtk git status" || gotDecision != DecisionAllow || gotReason != "RTK auto-rewrite" {
			t.Errorf("gate saw %q %q %q", gotArg, gotDecision, gotReason)
		}
	})
}
