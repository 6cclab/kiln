package settings

import (
	"strings"
	"testing"
)

// Regression tests for the verification of 0321523: places where kiln's
// hand-written lexers split a command line differently from bash. Each
// uses only Decide and IsReadOnlyCommand, so it runs against 0321523.

// notAllowed fails when cmd is allowed under p in manual mode.
func notAllowed(t *testing.T, p Permissions, cmd string) {
	t.Helper()
	if got := Decide(p, "bash", cmd, ModeManual); got == Allow {
		t.Errorf("%q under %v was allowed", cmd, p.Allow)
	}
}

// denied fails unless cmd is denied under p, in auto mode (where nothing
// but a rule stops it).
func denied(t *testing.T, p Permissions, cmd string) {
	t.Helper()
	if got := Decide(p, "bash", cmd, ModeAuto); got != Deny {
		t.Errorf("%q under deny %v = %v, want deny", cmd, p.Deny, got)
	}
}

// TestParser_EscapedMetaBeforeHash (CRITICAL): an escaped blank or
// operator byte before "#" does not start a comment; the "#" is part of
// the word, so a quote after it opens and the commands that follow run.
func TestParser_EscapedMetaBeforeHash(t *testing.T) {
	newPathFixture(t)
	for _, meta := range []string{" ", "\t", ";", "|", "&", "(", ")", "<", ">"} {
		cmd := "echo a\\" + meta + "#'\necho '; ln -s x PWNED; echo ' #'"
		notAllowed(t, Permissions{Allow: []string{"Bash(echo:*)"}}, cmd)
		denied(t, Permissions{Deny: []string{"Bash(ln:*)"}}, cmd)
		denied(t, Permissions{Deny: []string{"Read(.env)"}}, strings.Replace(cmd, "ln -s x PWNED", "cat .env", 1))
	}
}

// TestParser_HeredocBeforeContinuation (CRITICAL): a heredoc body is read
// line by line before any backslash-newline joining, so "echo \" ends the
// heredoc whose delimiter is "echo \", and the next line runs.
func TestParser_HeredocBeforeContinuation(t *testing.T) {
	newPathFixture(t)
	cmd := "cat <<'echo \\'\necho \\\nln -s x PWNED"
	notAllowed(t, Permissions{Allow: []string{"Bash(cat:*)", "Bash(echo:*)"}}, cmd)
	denied(t, Permissions{Deny: []string{"Bash(ln:*)"}}, cmd)
	denied(t, Permissions{Deny: []string{"Read(.env)"}}, "cat <<'echo \\'\necho \\\ncat .env")
}

// TestParser_EscapedRedirection (HIGH): "\>" is a literal ">", so the "&"
// or "|" after it is an operator, not part of a redirection.
func TestParser_EscapedRedirection(t *testing.T) {
	newPathFixture(t)
	for _, cmd := range []string{"echo a \\>& ln -s x PWNED", "echo a \\>| ln -s x PWNED", "echo a \\<& ln -s x PWNED"} {
		notAllowed(t, Permissions{Allow: []string{"Bash(echo:*)"}}, cmd)
		denied(t, Permissions{Deny: []string{"Bash(ln:*)"}}, cmd)
	}
}

// TestParser_DollarDollarQuote (HIGH): "$$" is the PID, so the "'" after
// it opens a plain single-quoted string, not an ANSI-C one.
func TestParser_DollarDollarQuote(t *testing.T) {
	newPathFixture(t)
	cmd := "echo $$'\\'; ln -s x PWNED; echo \\'"
	notAllowed(t, Permissions{Allow: []string{"Bash(echo:*)"}}, cmd)
	denied(t, Permissions{Deny: []string{"Bash(ln:*)"}}, cmd)
}

// TestParser_NestedCommandsMeetDenyRules (HIGH): a deny rule applies to a
// command wherever it runs — Claude Code: "including a command nested
// inside a subshell, a command substitution, or a control-flow body" —
// and past wrappers and any leading assignment.
func TestParser_NestedCommandsMeetDenyRules(t *testing.T) {
	newPathFixture(t)
	p := Permissions{Deny: []string{"Bash(ln:*)"}}
	for _, cmd := range []string{
		"echo $(ln -s x y)",
		"echo \"$(ln -s x y)\"",
		"echo `ln -s x y`",
		"(ln -s x y)",
		"{ ln -s x y; }",
		"if true; then ln -s x y; fi",
		"if ln -s x y; then :; fi",
		"for i in 1; do ln -s x y; done",
		"while false; do ln -s x y; done",
		"case a in a) ln -s x y;; esac",
		"f() { ln -s x y; }",
		"cat <(ln -s x y)",
		"echo >(ln -s x y)",
		"x=$(ln -s x y)",
		"[[ -n $(ln -s x y) ]]",
		"echo ${x:-$(ln -s x y)}",
		"time ln -s x y",
		"nohup ln -s x y",
		"command ln -s x y",
		"timeout 5 ln -s x y",
		"nice -n 5 ln -s x y",
		"FOO=1 ln -s x y",
		"FOO=$(id) ln -s x y",
		"! ln -s x y",
		"sudo -u root ln -s x y",
		"env A=1 ln -s x y",
		"/bin/ln -s x y",
		"xargs ln < list",
		"bash -c 'ln -s x y'",
		"eval ln -s x y",
		"cat <<EOF\n$(ln -s x y)\nEOF",
		"bash <<'EOF'\nln -s x y\nEOF",
	} {
		denied(t, p, cmd)
	}
}

// TestParser_BraceExpansion (found by TestBashDifferential): bash expands
// braces before it runs a command, so "{rm,-rf} x" is "rm -rf x" and
// ".e{n..n}v" is ".env".
func TestParser_BraceExpansion(t *testing.T) {
	newPathFixture(t)
	denied(t, Permissions{Deny: []string{"Bash(rm:*)"}}, "{rm,-rf} x")
	denied(t, Permissions{Deny: []string{"Bash(rm:*)"}}, "{r..r}m -rf x")
	denied(t, Permissions{Deny: []string{"Read(.env)"}}, "cat .e{n..n}v")
	denied(t, Permissions{Deny: []string{"Read(.env)"}}, "cat .e{n,x}v")
	notAllowed(t, Permissions{Allow: []string{"Bash(echo:*)"}}, "{rm,echo} x")
	if got := Decide(Permissions{Allow: []string{"Bash(echo:*)"}}, "bash", "{echo,rm} x", ModeManual); got != Allow {
		t.Errorf("{echo,rm} x (runs echo) = %v, want allow", got)
	}
}

// TestParser_FindAndXargs (MED): a prefix rule for find does not cover
// find running a command or deleting (Claude Code: "a Bash(find *) rule
// doesn't cover these forms"); only an exact rule does. A bare xargs is
// stripped, so Bash(xargs:*) does not cover what it runs.
func TestParser_FindAndXargs(t *testing.T) {
	newPathFixture(t)
	find := Permissions{Allow: []string{"Bash(find:*)"}}
	notAllowed(t, find, `find . -exec rm {} \;`)
	notAllowed(t, find, `find . -execdir rm {} +`)
	notAllowed(t, find, `find . -name '*.tmp' -delete`)
	if got := Decide(find, "bash", "find . -name '*.go'", ModeManual); got != Allow {
		t.Errorf("find . -name '*.go' under Bash(find:*) = %v, want allow", got)
	}
	exact := Permissions{Allow: []string{"Bash(find . -name 'x.tmp' -delete)"}}
	if got := Decide(exact, "bash", "find . -name 'x.tmp' -delete", ModeManual); got != Allow {
		t.Errorf("an exact rule for the find -delete = %v, want allow", got)
	}
	denied(t, Permissions{Deny: []string{"Bash(rm:*)"}}, `find . -exec rm {} \;`)

	notAllowed(t, Permissions{Allow: []string{"Bash(xargs:*)"}}, "xargs rm < list")
	if got := Decide(Permissions{Allow: []string{"Bash(grep:*)"}}, "bash", "xargs grep pattern", ModeManual); got != Allow {
		t.Errorf("xargs grep under Bash(grep:*) = %v, want allow (bare xargs is stripped)", got)
	}
	notAllowed(t, Permissions{Allow: []string{"Bash(grep:*)"}}, "xargs -n1 grep pattern")
}

// TestParser_Unparseable: a line kiln cannot parse, or longer than 10,000
// characters, is never approved by an allow rule.
func TestParser_Unparseable(t *testing.T) {
	newPathFixture(t)
	p := Permissions{Allow: []string{"Bash(npm *)", "Bash(echo *)"}}
	for _, cmd := range []string{"npm test &&", "npm test ||", "echo 'open", "echo $(npm test", "echo " + strings.Repeat("a", 10001)} {
		notAllowed(t, p, cmd)
	}
	// A deny rule still sees what can be made out of it.
	denied(t, Permissions{Deny: []string{"Bash(rm:*)"}}, "echo 'x' && rm -rf y &&")
}

// TestParser_AllowPastWrappers: Claude Code strips its safe wrappers and
// safe-variable assignments before matching an allow rule, and nothing
// else: an exec wrapper is approved only by an exact rule, and another
// assignment stops the rule.
func TestParser_AllowPastWrappers(t *testing.T) {
	newPathFixture(t)
	p := Permissions{Allow: []string{"Bash(npm test *)"}}
	for _, cmd := range []string{"npm test", "NODE_ENV=test npm test", "timeout 30 npm test", "nice -n 5 npm test", "nohup npm test", "time npm test -- x"} {
		if got := Decide(p, "bash", cmd, ModeManual); got != Allow {
			t.Errorf("%q = %v, want allow", cmd, got)
		}
	}
	for _, cmd := range []string{"FOO=1 npm test", "NODE_OPTIONS=--require=x npm test", "NODE_ENV=$(id) npm test", "timeout $(id) npm test"} {
		notAllowed(t, p, cmd)
	}
	notAllowed(t, Permissions{Allow: []string{"Bash(sudo *)"}}, "sudo rm -rf /")
	notAllowed(t, Permissions{Allow: []string{"Bash(watch *)"}}, "watch ls")
	notAllowed(t, Permissions{Allow: []string{"Bash(env *)"}}, "env rm -rf x")
	if got := Decide(Permissions{Allow: []string{"Bash(watch ls)"}}, "bash", "watch ls", ModeManual); got != Allow {
		t.Errorf("watch ls under its exact rule = %v, want allow", got)
	}
}

// TestParser_ReadOnlyCompound: a read-only command counts towards
// approving a line when it stays in the working directory (Claude Code:
// "a compound command ... runs without a prompt when each part
// qualifies"), so an allow rule for the rest is enough.
func TestParser_ReadOnlyCompound(t *testing.T) {
	newPathFixture(t)
	p := Permissions{Allow: []string{"Bash(npm test)"}}
	if got := Decide(p, "bash", "git status && npm test", ModeManual); got != Allow {
		t.Errorf("git status && npm test = %v, want allow", got)
	}
	notAllowed(t, p, "cat /etc/passwd && npm test")
	notAllowed(t, p, "cat ../x && npm test")
	notAllowed(t, p, "touch x && npm test")
}

// TestParser_ReadOnlyClaudeCodeCases: the read-only classification's
// Claude Code exceptions.
func TestParser_ReadOnlyClaudeCodeCases(t *testing.T) {
	yes := []string{
		"ls *.go",
		"wc -l src/*.py",
		"cd . && git status",
		"cd app; grep -r pattern . 2>/dev/null",
		"echo a\\ b",
	}
	no := []string{
		"find *.go",
		"sort *",
		"git log *",
		"sed -n 1p *",
		"cd sub && git status",
		"cd sub && git -C . log",
		"cd sub && wc -l < f",
		"PATH=/tmp ls",
		"IFS=x ls",
		"ls &",
		"(ls)",
		"ls\nrm x",
		"echo hi # \\\nrm x",
	}
	for _, c := range yes {
		if !IsReadOnlyCommand(c) {
			t.Errorf("IsReadOnlyCommand(%q) = false, want true", c)
		}
	}
	for _, c := range no {
		if IsReadOnlyCommand(c) {
			t.Errorf("IsReadOnlyCommand(%q) = true, want false", c)
		}
	}
}

// TestParser_BackgroundShellRules: bash_background is the same tool as
// bash to Bash rules.
func TestParser_BackgroundShellRules(t *testing.T) {
	newPathFixture(t)
	if got := Decide(Permissions{Deny: []string{"Bash(rm *)"}}, "bash_background", "sleep 1; rm -rf x", ModeAuto); got != Deny {
		t.Errorf("bash_background under Bash(rm *) deny = %v, want deny", got)
	}
	if got := Decide(Permissions{Allow: []string{"Bash(npm run *)"}}, "bash_background", "npm run dev", ModeManual); got != Allow {
		t.Errorf("bash_background under Bash(npm run *) = %v, want allow", got)
	}
}
