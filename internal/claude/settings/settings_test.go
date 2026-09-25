package settings

import (
	"os"
	"path/filepath"
	"testing"
)

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

// TestLoadSettingsModelRoles covers modelRoles's merge: last-non-empty-wins
// per role key across user -> project -> local, mirroring model/effortLevel
// but keyed instead of scalar.
func TestLoadSettingsModelRoles(t *testing.T) {
	writeJSON := func(t *testing.T, path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("merges per key across scopes, later non-empty wins", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		cwd := t.TempDir()

		writeJSON(t, filepath.Join(home, ".claude", "settings.json"),
			`{"modelRoles": {"fast": "ollama/qwen3.8", "heavy": "anthropic/claude-opus-4-1"}}`)
		writeJSON(t, filepath.Join(cwd, ".claude", "settings.json"),
			`{"modelRoles": {"fast": "anthropic/claude-haiku-4-5"}}`)
		writeJSON(t, filepath.Join(cwd, ".claude", "settings.local.json"),
			`{"modelRoles": {"structured": "anthropic/claude-sonnet-4-5"}}`)

		got := LoadSettings(cwd, LoadOptions{})
		want := map[string]string{
			"fast":       "anthropic/claude-haiku-4-5",  // project overrode user
			"heavy":      "anthropic/claude-opus-4-1",   // inherited from user, untouched
			"structured": "anthropic/claude-sonnet-4-5", // added by local
		}
		if len(got.ModelRoles) != len(want) {
			t.Fatalf("got %v, want %v", got.ModelRoles, want)
		}
		for k, v := range want {
			if got.ModelRoles[k] != v {
				t.Errorf("role %q = %q, want %q", k, got.ModelRoles[k], v)
			}
		}
	})

	t.Run("an empty string for a role does not clear an earlier scope's value", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		cwd := t.TempDir()

		writeJSON(t, filepath.Join(home, ".claude", "settings.json"),
			`{"modelRoles": {"fast": "ollama/qwen3.8"}}`)
		writeJSON(t, filepath.Join(cwd, ".claude", "settings.json"),
			`{"modelRoles": {"fast": ""}}`)

		got := LoadSettings(cwd, LoadOptions{})
		if got.ModelRoles["fast"] != "ollama/qwen3.8" {
			t.Errorf("fast = %q, want inherited value preserved", got.ModelRoles["fast"])
		}
	})

	t.Run("absent modelRoles across every scope stays nil", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		cwd := t.TempDir()
		writeJSON(t, filepath.Join(cwd, ".claude", "settings.json"), `{"model": "anthropic/claude-sonnet-4-5"}`)

		got := LoadSettings(cwd, LoadOptions{})
		if got.ModelRoles != nil {
			t.Errorf("ModelRoles = %v, want nil", got.ModelRoles)
		}
	})
}
