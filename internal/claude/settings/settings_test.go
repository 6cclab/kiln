package settings

import "testing"

// Every case below is a real format found in ~/.claude/settings.json. Three
// of them were bugs that shipped in the TS reference: case-sensitivity, the
// colon form, and bare mcp__ prefixes. All three failed *open* - every rule
// missed, so the gate silently degraded into prompting for everything.
func TestMatchesRule(t *testing.T) {
	cases := []struct {
		name       string
		rule, tool string
		arg        string
		want       bool
	}{
		{"bare tool name matches", "Read", "read", "", true},
		{"bare tool name does not match other tool", "Read", "bash", "x", false},
		{"case-insensitive tool", "Read", "read", "", true},
		{"case-insensitive rule", "BASH", "bash", "ls", true},
		{"colon form", "Bash(find:*)", "bash", "find . -name '*.go'", true},
		{"colon form rejects mismatch", "Bash(find:*)", "bash", "rm -rf /", false},
		{"colon form matches bare command", "Bash(ls:*)", "bash", "ls", true},
		{"space glob form", "Bash(git *)", "bash", "git status", true},
		{"space glob form rejects mismatch", "Bash(git *)", "bash", "npm test", false},
		{"bare mcp__ prefix matches", "mcp__homelab", "mcp__homelab-kb__hk_search", "", true},
		{"bare mcp__ prefix rejects other server", "mcp__homelab", "mcp__grafana__query", "", false},
		{"regex metachar escaped: dot not wildcard", "Bash(npm run build.sh)", "bash", "npm run buildXsh", false},
		{"regex metachar escaped: literal dot matches", "Bash(npm run build.sh)", "bash", "npm run build.sh", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := MatchesRule(c.rule, c.tool, c.arg); got != c.want {
				t.Errorf("MatchesRule(%q, %q, %q) = %v, want %v", c.rule, c.tool, c.arg, got, c.want)
			}
		})
	}
}

func TestDecide(t *testing.T) {
	permissions := Permissions{Allow: []string{"Read", "Bash(ls:*)"}, Deny: []string{"Bash(rm:*)"}}

	t.Run("deny wins over allow", func(t *testing.T) {
		permissive := Permissions{Allow: []string{"Bash"}, Deny: []string{"Bash(rm:*)"}}
		if got := Decide(permissive, "bash", "rm -rf /", ModeDontAsk); got != Deny {
			t.Errorf("got %v, want deny", got)
		}
	})

	t.Run("allows what the rules allow", func(t *testing.T) {
		if got := Decide(permissions, "read", "f.ts", ModeManual); got != Allow {
			t.Errorf("got %v, want allow", got)
		}
		if got := Decide(permissions, "bash", "ls -la", ModeManual); got != Allow {
			t.Errorf("got %v, want allow", got)
		}
	})

	t.Run("asks for anything unmatched in manual mode", func(t *testing.T) {
		if got := Decide(permissions, "write", "f.ts", ModeManual); got != Ask {
			t.Errorf("got %v, want ask", got)
		}
	})

	t.Run("acceptEdits auto-approves edits but still asks for other tools", func(t *testing.T) {
		if got := Decide(permissions, "edit", "f.ts", ModeAcceptEdits); got != Allow {
			t.Errorf("edit: got %v, want allow", got)
		}
		if got := Decide(permissions, "write", "f.ts", ModeAcceptEdits); got != Allow {
			t.Errorf("write: got %v, want allow", got)
		}
		if got := Decide(permissions, "bash", "curl evil.com", ModeAcceptEdits); got != Ask {
			t.Errorf("bash: got %v, want ask", got)
		}
	})

	t.Run("plan mode refuses mutations outright rather than prompting", func(t *testing.T) {
		if got := Decide(permissions, "write", "f.ts", ModePlan); got != Deny {
			t.Errorf("write: got %v, want deny", got)
		}
		if got := Decide(permissions, "read", "f.ts", ModePlan); got != Allow {
			t.Errorf("read: got %v, want allow", got)
		}
	})

	t.Run("bypassPermissions still honours explicit denials", func(t *testing.T) {
		if got := Decide(permissions, "bash", "rm -rf /", ModeBypassPermissions); got != Deny {
			t.Errorf("got %v, want deny", got)
		}
		if got := Decide(permissions, "write", "f.ts", ModeBypassPermissions); got != Allow {
			t.Errorf("got %v, want allow", got)
		}
	})
}

func TestAutoMode(t *testing.T) {
	none := Permissions{}

	t.Run("allows everything, including writes", func(t *testing.T) {
		for _, tool := range []string{"read", "grep", "write", "edit", "bash"} {
			if got := Decide(none, tool, "", ModeAuto); got != Allow {
				t.Errorf("%s still asked: got %v", tool, got)
			}
		}
	})

	t.Run("is still overridden by an explicit deny", func(t *testing.T) {
		withDeny := none
		withDeny.Deny = []string{"Write"}
		if got := Decide(withDeny, "write", "", ModeAuto); got != Deny {
			t.Errorf("got %v, want deny", got)
		}
		withDeny2 := none
		withDeny2.Deny = []string{"Bash(rm:*)"}
		if got := Decide(withDeny2, "bash", "rm -rf /", ModeAuto); got != Deny {
			t.Errorf("got %v, want deny", got)
		}
	})

	t.Run("differs from manual, which is the point", func(t *testing.T) {
		if Decide(none, "write", "", ModeAuto) == Decide(none, "write", "", ModeManual) {
			t.Errorf("auto and manual produced the same decision")
		}
	})
}

func TestPlanModeAllowsExitPlanMode(t *testing.T) {
	perms := Permissions{}
	if got := Decide(perms, "exit_plan_mode", "", ModePlan); got != Allow {
		t.Fatalf("plan mode must allow exit_plan_mode, got %s", got)
	}
	if got := Decide(perms, "write", "x", ModePlan); got != Deny {
		t.Fatalf("plan mode must still deny write, got %s", got)
	}
}
