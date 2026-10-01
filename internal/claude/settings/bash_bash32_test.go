package settings

import "testing"

// Regression tests for the verification of 080b814: places where the
// parser and bash 3.2 (the /bin/bash kiln runs on macOS) read a line
// differently, read-only options that write or run, and redirects outside
// the workspace. Each uses only Decide and IsReadOnlyCommand.

// notApproved fails when cmd is allowed under allow in manual mode, or is
// read-only.
func notApproved(t *testing.T, allow []string, cmd string) {
	t.Helper()
	if got := Decide(Permissions{Allow: allow}, "bash", cmd, ModeManual); got == Allow {
		t.Errorf("%q under %v was allowed", cmd, allow)
	}
	if IsReadOnlyCommand(cmd) {
		t.Errorf("%q is read-only", cmd)
	}
}

// TestBash32_CarriageReturn (CRITICAL A, B): bash reads a CR as a word
// byte, so "\" CR LF is no continuation and "a<CR>#" is one word; the
// parser reads both the other way. A line with a CR is unparseable.
func TestBash32_CarriageReturn(t *testing.T) {
	newPathFixture(t)
	for _, cmd := range []string{
		"ls \\\r\nln -s x PWNED",
		"echo a\r# ; ln -s x PWNED",
		"ls\r",
	} {
		notApproved(t, []string{"Bash(ls:*)", "Bash(echo:*)"}, cmd)
	}
	denied(t, Permissions{Deny: []string{"Bash(rm *)"}}, "ls \\\r\nrm -rf x")
	denied(t, Permissions{Deny: []string{"Bash(ln:*)"}}, "echo a\r# ; ln -s x PWNED")
}

// TestBash32_HeredocExpansionAcrossDelimiter (HIGH C): bash 3.2 ends an
// unquoted heredoc at the first delimiter line even inside an expansion
// that spans it, so the line after runs.
func TestBash32_HeredocExpansionAcrossDelimiter(t *testing.T) {
	newPathFixture(t)
	for _, cmd := range []string{
		"cat <<E\n${x:-\nE\nln -s x PWNED\n}\nE",
		"cat <<E\n$(echo\nE\nln -s x PWNED\n)\nE",
	} {
		notApproved(t, []string{"Bash(cat:*)", "Bash(echo:*)"}, cmd)
		if got := Decide(Permissions{Deny: []string{"Bash(ln:*)"}}, "bash", cmd, ModeAuto); got == Allow {
			t.Errorf("%q under deny Bash(ln:*) was allowed", cmd)
		}
	}
}

// TestBash32_CaseInsideSubstitution (HIGH D): bash 3.2 ends "$(" at the
// first ")" of a case pattern.
func TestBash32_CaseInsideSubstitution(t *testing.T) {
	newPathFixture(t)
	allow := []string{"Bash(echo:*)", "Bash(true:*)"}
	for _, cmd := range []string{
		"echo $(case x in y) true;; esac)",
		"echo `case x in y) true;; esac`",
		"echo <(case x in y) true;; esac)",
	} {
		notApproved(t, allow, cmd)
	}
}

// TestBash32_Arithmetic (HIGH E): a quoted string in arithmetic, or in an
// array subscript, is evaluated as code, so no allow rule approves a line
// with arithmetic or a subscript.
func TestBash32_Arithmetic(t *testing.T) {
	newPathFixture(t)
	allow := []string{"Bash(echo:*)", "Bash(cat:*)", "Bash(unset:*)", "Bash(printf:*)", "Bash(true:*)", "Bash(test:*)"}
	for _, cmd := range []string{
		"echo $(( 'a[$(ln -s x PWNED)]' ))",
		"echo $(( 1 + 1 ))",
		"echo $[ 'a[$(ln -s x PWNED)]' ]",
		"echo ${x['a[$(ln -s x PWNED)]']}",
		"a['$(ln -s x PWNED)']=1",
		"cat <<E\n$(( 'a[$(ln -s x PWNED)]' ))\nE",
		"unset 'a[$(ln -s x PWNED)]'",
		"printf -v 'a[$(ln -s x PWNED)]' x",
		"test -v 'a[$(ln -s x PWNED)]'",
		"(( a['$(ln -s x PWNED)'] ))",
		"let 'a[$(ln -s x PWNED)]'",
		"for (( i = 0; i < 1; i++ )); do true; done",
		"[[ 1 -eq 'a[$(ln -s x PWNED)]' ]] && true",
	} {
		notApproved(t, allow, cmd)
	}
}

// TestBash32_ReadOnlyOptions (F): read-only programs' options that write a
// file or run a program, in every spelling getopt accepts.
func TestBash32_ReadOnlyOptions(t *testing.T) {
	no := []string{
		"printf -v x hi", "printf -vx hi",
		"sort -o out in", "sort -oout in", "sort -mo out in", "sort --o=out in", "sort --outp=out in", "sort --output out in",
		"sort --compress-program=sh in", "sort --compress=sh in",
		"git grep -O x", "git grep -nO x", "git grep --open-files-in-pager x", "git grep --open x",
		"git log --outp=x", "git cat-file --filters HEAD:x", "git show --ext-diff",
		"go list -toolexec x ./...", "go list -exec x ./...", "go list --toolexec=x", "go version -toolexec x",
		"file -C", "file -m magic x", "file -bm magic x", "file --magic-file magic x", "file --magic=m x",
		"file -f list", "file --files-from list",
		"tree -o out", "tree -ao out", "tree -R -H . x", "tree -RH . x",
		"date -s 2020-01-01", "date -us 2020-01-01", "date --set=x", "date --se x",
		"rg --hostname-bin=x y", "rg --pre=sh x",
	}
	for _, c := range no {
		if IsReadOnlyCommand(c) {
			t.Errorf("IsReadOnlyCommand(%q) = true, want false", c)
		}
	}
	yes := []string{
		"printf '%s\\n' x", "sort -n x", "sort -u -k2,2 x", "file x", "file -b x", "tree -L 2",
		"date +%s", "date -u", "git grep -n x", "git log --oneline -3", "go list ./...", "go version", "rg -n x",
	}
	for _, c := range yes {
		if !IsReadOnlyCommand(c) {
			t.Errorf("IsReadOnlyCommand(%q) = false, want true", c)
		}
	}
}

// TestBash32_RedirectOutsideWorkspace (G): a rule allows the command, not
// the file it writes; an output redirect to ~, outside the working
// directory, or to a path that expands needs approval.
func TestBash32_RedirectOutsideWorkspace(t *testing.T) {
	newPathFixture(t)
	allow := []string{"Bash(echo:*)"}
	for _, cmd := range []string{
		"echo hi >> ~/.zshrc",
		"echo hi > $HOME/.bashrc",
		"echo hi > /etc/hosts",
		"echo hi > ../x",
		"echo hi > \"$f\"",
		"echo hi &> ~/x",
	} {
		if got := Decide(Permissions{Allow: allow}, "bash", cmd, ModeManual); got == Allow {
			t.Errorf("%q under %v was allowed", cmd, allow)
		}
	}
	for _, cmd := range []string{"echo hi > out.txt", "echo hi >> sub/log.txt", "echo hi > /dev/null 2>&1"} {
		if got := Decide(Permissions{Allow: allow}, "bash", cmd, ModeManual); got != Allow {
			t.Errorf("%q under %v = %v, want allow", cmd, allow, got)
		}
	}
}

// TestBash32_ExactRuleWithWrapper: an allow rule written with the wrapper
// still matches the command as written.
func TestBash32_ExactRuleWithWrapper(t *testing.T) {
	newPathFixture(t)
	if got := Decide(Permissions{Allow: []string{"Bash(timeout 3 pnpm start:*)"}}, "bash", "timeout 3 pnpm start", ModeManual); got != Allow {
		t.Errorf("timeout 3 pnpm start under Bash(timeout 3 pnpm start:*) = %v, want allow", got)
	}
	notApproved(t, []string{"Bash(timeout 3 sudo:*)"}, "timeout 3 sudo rm -rf /")
	notApproved(t, []string{"Bash(xargs:*)"}, "xargs rm < list")
}

// A nested shell may be dash (/bin/sh on Debian/Ubuntu), which reads $'…'
// literally, so kiln can't know the words: the line is unknown, and an
// allow rule must not approve it. Found by TestBashDifferential on Linux CI.
func TestBash32_NestedShellAnsiQuoting(t *testing.T) {
	p := Permissions{Allow: []string{"Bash(sh -c:*)", "Bash(cmdc:*)"}}
	for _, line := range []string{
		`sh -c 'cmdc $'"'"'t\x41'"'"''`,
		`sh -c "cmdc \$\"x\""`,
		`eval "cmdc \$'x'"`,
	} {
		if got := Decide(p, "bash", line, ModeManual); got == Allow {
			t.Errorf("%q: got Allow, want not Allow", line)
		}
	}
}
