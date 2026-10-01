package settings

import (
	"reflect"
	"testing"
)

// TestBashDontAskRules checks the rules a "don't ask again" saves for a
// command line: one per command that still needs approval, read-only and
// already-allowed ones skipped, duplicates merged, wrappers and shells
// never a prefix, and nothing at all when no safe set exists.
func TestBashDontAskRules(t *testing.T) {
	cwd := t.TempDir()
	cases := []struct {
		name  string
		allow []string
		cmd   string
		want  []string
	}{
		{name: "compound, read-only part skipped",
			cmd: "git status && npm test && make build", want: []string{"npm test *", "make build *"}},
		{name: "single command",
			cmd: "npm test -- upload", want: []string{"npm test *"}},
		{name: "already allowed part skipped",
			allow: []string{"Bash(npm test *)"},
			cmd:   "npm test && make build", want: []string{"make build *"}},
		{name: "duplicates merged",
			cmd: "npm test -- a; npm test -- b && npm test", want: []string{"npm test *"}},
		{name: "no subcommand word: exact",
			cmd: "echo hi | tee hi.txt", want: []string{"tee hi.txt"}},
		{name: "cd inside the workspace skipped",
			cmd: "cd api && npm test", want: []string{"npm test *"}},
		{name: "safe wrapper stripped",
			cmd: "timeout 30 npm test", want: []string{"npm test *"}},
		{name: "safe variable stripped",
			cmd: "NODE_ENV=test npm test", want: []string{"npm test *"}},
		{name: "other variable kept: exact",
			cmd: "FOO=1 npm test", want: []string{"FOO=1 npm test"}},
		{name: "shell is never a prefix",
			cmd: "sh -c 'npm run build'", want: []string{"sh -c 'npm run build'", "npm run *"}},
		{name: "exec wrapper is never a prefix",
			cmd: "env FOO=1 npm test", want: []string{"env FOO=1 npm test"}},
		{name: "sudo is never a prefix",
			cmd: "sudo apt install jq", want: []string{"sudo apt install jq"}},
		{name: "xargs runs its argument as the command",
			cmd: "xargs make build", want: []string{"make build *"}},
		{name: "five rules",
			cmd:  "a1 x && a2 x && a3 x && a4 x && a5 x",
			want: []string{"a1 x *", "a2 x *", "a3 x *", "a4 x *", "a5 x *"}},
		{name: "more than five: none",
			cmd: "a1 x && a2 x && a3 x && a4 x && a5 x && a6 x"},
		{name: "non-literal argument: none",
			cmd: "npm test $FILE"},
		{name: "non-literal command: none",
			cmd: "$CMD build"},
		{name: "command substitution: none",
			cmd: "npm test $(cat list)"},
		{name: "unparseable: none",
			cmd: "npm test &&"},
		{name: "glob in an exact rule: none",
			cmd: "rm *.tmp"},
		{name: "write outside the workspace: none",
			cmd: "npm test > /tmp/out.txt"},
		{name: "declaration has no rule shape: none",
			cmd: "export X=1 && npm test"},
		{name: "everything already allowed: none",
			allow: []string{"Bash(npm test *)"},
			cmd:   "npm test"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := BashDontAskRules(Permissions{Allow: c.allow}, cwd, "bash", c.cmd)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("BashDontAskRules(%q) = %q, want %q", c.cmd, got, c.want)
			}
		})
	}
}

// TestBashDontAskRulesHonoured checks the property the prompt relies on:
// with the returned rules saved, the same line is allowed, and so is each
// command the rules name on its own.
func TestBashDontAskRulesHonoured(t *testing.T) {
	cwd := t.TempDir()
	for _, cmd := range []string{
		"git status && npm test && make build",
		"echo hi | tee hi.txt",
		"sh -c 'npm run build'",
		"FOO=1 npm test",
		"sudo apt install jq",
	} {
		rules := BashDontAskRules(Permissions{}, cwd, "bash", cmd)
		if rules == nil {
			t.Fatalf("no rules for %q", cmd)
		}
		p := Permissions{Allow: BashRules(rules)}
		if got := DecideIn(p, cwd, "bash", cmd, ModeManual); got != Allow {
			t.Errorf("%q with %q saved: %v, want allow", cmd, rules, got)
		}
	}
	p := Permissions{Allow: BashRules(BashDontAskRules(Permissions{}, cwd, "bash", "git status && npm test && make build"))}
	for _, cmd := range []string{"npm test", "npm test -- upload", "make build"} {
		if got := DecideIn(p, cwd, "bash", cmd, ModeManual); got != Allow {
			t.Errorf("later %q: %v, want allow", cmd, got)
		}
	}
	if got := DecideIn(p, cwd, "bash", "make clean", ModeManual); got != Ask {
		t.Errorf("make clean: %v, want ask (not covered by the saved rules)", got)
	}
}

// TestBashDontAskRulesGuarded: a line an ask or deny rule matches gets no
// rules, since saving them would not stop the prompt.
func TestBashDontAskRulesGuarded(t *testing.T) {
	cwd := t.TempDir()
	for _, p := range []Permissions{
		{Ask: []string{"Bash(make *)"}},
		{Deny: []string{"Bash(make *)"}},
	} {
		if got := BashDontAskRules(p, cwd, "bash", "npm test && make build"); got != nil {
			t.Errorf("with %+v: %q, want none", p, got)
		}
	}
	if got := BashDontAskRules(Permissions{}, cwd, "read", "x"); got != nil {
		t.Errorf("non-bash tool: %q, want none", got)
	}
}
