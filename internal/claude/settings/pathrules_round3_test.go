package settings

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Regression tests for the verification review of 07333a9. They use only
// Decide, DecideIn and BashSegments, so each can be run against that
// commit.

// linkTrick builds sshl -> ~/.ssh and evil -> "sshl/.." in the project:
// evil is ~ to the kernel, the project to a lexical cleaner.
func linkTrick(t *testing.T, f pathFixture) {
	t.Helper()
	if err := os.MkdirAll(f.h(".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.h(".ssh"), f.p("sshl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sshl/..", f.p("evil")); err != nil {
		t.Fatal(err)
	}
}

// TestRound3_DotDotLinkTarget (HIGH 1): a ".." inside a link target is
// walked physically.
func TestRound3_DotDotLinkTarget(t *testing.T) {
	f := newPathFixture(t)
	linkTrick(t, f)
	p := Permissions{Deny: []string{"Edit(~/.ssh/**)"}}
	if got := Decide(p, "write", f.p("evil/.ssh/authorized_keys"), ModeAcceptEdits); got != Deny {
		t.Errorf("write evil/.ssh/authorized_keys = %v, want deny", got)
	}
	if got := bashVerdict(p, "echo k >> evil/.ssh/authorized_keys"); got != Deny {
		t.Errorf("bash append = %v, want deny", got)
	}
}

// TestRound3_BashPhysicalDotDot (MED 2): a bash operand's ".." after a
// link climbs out of the link's target, as open(2) does.
func TestRound3_BashPhysicalDotDot(t *testing.T) {
	f := newPathFixture(t)
	linkTrick(t, f)
	p := Permissions{Deny: []string{"Read(~/.ssh/**)"}}
	for _, cmd := range []string{"cat sshl/../.ssh/id_rsa", "cd sshl && cat ../.ssh/id_rsa", "head evil/.ssh/config"} {
		if got := bashVerdict(p, cmd); got != Deny {
			t.Errorf("%q = %v, want deny", cmd, got)
		}
	}
}

// TestRound3_AnsiCQuoting (MED 3): $'…' and $"…" are quoting, not
// variables.
func TestRound3_AnsiCQuoting(t *testing.T) {
	newPathFixture(t)
	p := Permissions{Deny: []string{"Read(.env)"}}
	for _, cmd := range []string{`cat $'.env'`, `cat $'\x2eenv'`, `cat $'\056env'`, `cat $".env"`, `cat $'.e'nv`} {
		if got := bashVerdict(p, cmd); got != Deny {
			t.Errorf("%s = %v, want deny", cmd, got)
		}
	}
	if got := bashVerdict(p, `cat $'.env`); got != Ask {
		t.Errorf("an unclosed $'… = %v, want ask", got)
	}
	segs, _ := BashSegments(`echo $'a\';b' && ls`)
	if len(segs) != 2 {
		t.Errorf(`BashSegments split inside $'…': %q`, segs)
	}
}

// TestRound3_UnsureNeverWeakensDeny (MED 4): an unknowable operand can
// lift Allow to Ask, never turn a Deny (plan mode's, or a rule's) into a
// prompt.
func TestRound3_UnsureNeverWeakensDeny(t *testing.T) {
	newPathFixture(t)
	p := Permissions{Deny: []string{"Read(.env)"}}
	cmd := "rm -rf $TMPDIR/foo"
	want := map[PermissionMode]Decision{
		ModePlan:              Deny,
		ModeManual:            Ask,
		ModeAcceptEdits:       Ask,
		ModeAuto:              Ask,
		ModeDontAsk:           Ask, // the gate refuses it
		ModeBypassPermissions: Ask,
	}
	for mode, w := range want {
		if got := Decide(p, "bash", cmd, mode); got != w {
			t.Errorf("%s: %q = %v, want %v", mode, cmd, got, w)
		}
	}
	if got := Decide(Permissions{Deny: []string{"Read(.env)", "Bash(rm *)"}}, "bash", cmd, ModeAuto); got != Deny {
		t.Errorf("a Bash deny rule with an unknowable operand = %v, want deny", got)
	}
}

// TestRound3_GlobsSkipDotfiles (MED 5): *, ? and [!…] do not match a
// leading "."; ".*" does.
func TestRound3_GlobsSkipDotfiles(t *testing.T) {
	f := newPathFixture(t)
	mkfile(t, f.p(".env"))
	mkfile(t, f.p("a.txt"))
	p := Permissions{Deny: []string{"Read(.env)"}}
	for _, cmd := range []string{"ls *", "wc -l *", "grep foo *", "cat ?env", "cat [!a]env", "cat */*"} {
		if got := bashVerdict(p, cmd); got != Allow {
			t.Errorf("%q = %v, want allow (no dotfile matched)", cmd, got)
		}
	}
	for _, cmd := range []string{"cat .*", "cat .e?v"} {
		if got := bashVerdict(p, cmd); got != Deny {
			t.Errorf("%q = %v, want deny", cmd, got)
		}
	}
}

// TestRound3_Heredocs (MED 6): a heredoc body is input, not commands;
// only an unquoted-delimiter body's substitutions run.
func TestRound3_Heredocs(t *testing.T) {
	newPathFixture(t)
	p := Permissions{Deny: []string{"Read(.env)"}}
	allow := []string{
		"cat <<EOF\ncat .env\nEOF",
		"cat <<'EOF'\n$(cat .env)\nEOF",
		"cat <<\"EOF\"\n`cat .env`\nEOF",
		"cat <<-EOF\n\tcat .env\n\tEOF",
		"cat <<A <<B\ncat .env\nA\ncat .env\nB\necho ok",
		"python3 - <<EOF\nopen('.env')\nEOF",
	}
	for _, cmd := range allow {
		if got := bashVerdict(p, cmd); got != Allow {
			t.Errorf("%q = %v, want allow", cmd, got)
		}
	}
	deny := []string{
		"cat <<EOF\n$(cat .env)\nEOF",
		"cat <<EOF\nx\nEOF\ncat .env",
		"cat <<A <<'B'\nx\nA\ny\nB\ncat .env",
	}
	for _, cmd := range deny {
		if got := bashVerdict(p, cmd); got != Deny {
			t.Errorf("%q = %v, want deny", cmd, got)
		}
	}
	// Bash rules still see the line as before.
	if got := bashVerdict(Permissions{Deny: []string{"Bash(rm *)"}}, "cat <<EOF\nrm -rf /\nEOF"); got != Deny {
		t.Errorf("Bash(rm *) on a heredoc = %v, want deny (unchanged)", got)
	}
}

// TestRound3_GlobCap (MED 7): expanding a huge glob is capped, quickly,
// and the operand is then unknowable.
func TestRound3_GlobCap(t *testing.T) {
	newPathFixture(t)
	p := Permissions{Deny: []string{"Read(.env)"}}
	start := time.Now()
	got := bashVerdict(p, "cat /*/*/*/*/*")
	if took := time.Since(start); took > time.Second {
		t.Errorf("took %v", took)
	}
	if got != Ask {
		t.Errorf("an over-budget glob = %v, want ask", got)
	}
}

// TestRound3_ShellDashC (LOW-MED 8): bash -c / sh -c / eval run a command
// line; a command word that is an expansion is unknowable.
func TestRound3_ShellDashC(t *testing.T) {
	newPathFixture(t)
	p := Permissions{Deny: []string{"Read(.env)"}}
	for _, cmd := range []string{
		"bash -c 'cat .env'", `sh -c "cat .env"`, "bash -lc 'cat .env'", "zsh -ec 'head .env'",
		"eval cat .env", `eval "cat .env"`, "bash .env", "sh -c 'sh -c \"cat .env\"'",
	} {
		if got := bashVerdict(p, cmd); got != Deny {
			t.Errorf("%q = %v, want deny", cmd, got)
		}
	}
	for _, cmd := range []string{"cat${IFS}.env", "$CMD .env", "bash -c \"$X\"", "eval $X"} {
		if got := bashVerdict(p, cmd); got != Ask {
			t.Errorf("%q = %v, want ask", cmd, got)
		}
	}
}

// TestRound3_AskRulesReachBash (LOW 9): a Read/Edit ask rule asks for the
// files a bash command names.
func TestRound3_AskRulesReachBash(t *testing.T) {
	newPathFixture(t)
	if got := bashVerdict(Permissions{Ask: []string{"Read(.env)"}}, "cat .env"); got != Ask {
		t.Errorf("Read(.env) ask on cat .env = %v, want ask", got)
	}
	if got := bashVerdict(Permissions{Ask: []string{"Edit(out.txt)"}}, "echo x > out.txt"); got != Ask {
		t.Errorf("Edit(out.txt) ask on a redirect = %v, want ask", got)
	}
	if got := bashVerdict(Permissions{Ask: []string{"Read(.env)"}}, "cat README.md"); got != Allow {
		t.Errorf("an unrelated file = %v, want allow", got)
	}
}

// TestRound3_BypassOrder (LOW 10): deny, then ask, then bypass: an
// explicit ask rule still asks in bypassPermissions.
func TestRound3_BypassOrder(t *testing.T) {
	f := newPathFixture(t)
	cases := []struct {
		name, tool, arg string
		p               Permissions
		want            Decision
	}{
		{"bash ask", "bash", "git push", Permissions{Ask: []string{"Bash(git push*)"}}, Ask},
		{"path ask", "edit", f.p("a.go"), Permissions{Ask: []string{"Edit(a.go)"}}, Ask},
		{"ask beats allow", "edit", f.p("a.go"), Permissions{Allow: []string{"Edit"}, Ask: []string{"Edit(a.go)"}}, Ask},
		{"deny", "bash", "rm x", Permissions{Deny: []string{"Bash(rm *)"}}, Deny},
		{"nothing", "bash", "make", Permissions{}, Allow},
	}
	for _, c := range cases {
		if got := Decide(c.p, c.tool, c.arg, ModeBypassPermissions); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// TestRound3_AnchorReresolved (LOW 11): a rule's anchor is resolved to its
// real location on each decision, not once when the rules were compiled.
func TestRound3_AnchorReresolved(t *testing.T) {
	f := newPathFixture(t)
	secret := t.TempDir()
	link := filepath.Join(t.TempDir(), "vault")
	p := Permissions{Deny: []string{"Read(/" + link + "/**)"}}
	target := filepath.Join(secret, "k")
	if DecideIn(p, f.proj, "read", target, ModeAuto) == Deny {
		t.Fatal("denied before the link existed")
	}
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	if got := DecideIn(p, f.proj, "read", target, ModeAuto); got != Deny {
		t.Errorf("after vault became a link to %s, reading its real location = %v, want deny", secret, got)
	}
}
