package settings

import (
	"os"
	"strings"
	"testing"
)

// Regression tests for the adversarial review of the first path-rules
// commit. They use Decide, BashSegments and MatchesRule (API older than
// that commit) wherever they can, so each can be run against it.

// bashVerdict is Decide on a bash command in auto mode, where no rule
// means Allow: Deny and Ask therefore come only from rules.
func bashVerdict(p Permissions, cmd string) Decision {
	return Decide(p, "bash", cmd, ModeAuto)
}

// TestReview_DanglingSymlink (HIGH 1): a link whose target does not exist
// yet resolves to that target, so writing through it is the target's
// write.
func TestReview_DanglingSymlink(t *testing.T) {
	f := newPathFixture(t)
	if err := os.MkdirAll(f.h(".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.h(".ssh/authorized_keys"), f.p("dangle")); err != nil {
		t.Fatal(err)
	}
	p := Permissions{Deny: []string{"Edit(~/.ssh/**)"}}
	if Decide(p, "write", f.p("dangle"), ModeAcceptEdits) != Deny {
		t.Error("write through a dangling link into ~/.ssh was not denied")
	}
	if bashVerdict(p, "echo key > dangle") != Deny {
		t.Error("bash redirect through a dangling link into ~/.ssh was not denied")
	}
	// A relative dangling link, two hops.
	if err := os.Symlink("dangle", f.p("hop")); err != nil {
		t.Fatal(err)
	}
	if Decide(p, "write", f.p("hop"), ModeAcceptEdits) != Deny {
		t.Error("write through two links to a missing target was not denied")
	}
}

// TestReview_ClobberRedirect (MED 3): ">|" is a redirection, not a pipe.
func TestReview_ClobberRedirect(t *testing.T) {
	newPathFixture(t)
	p := Permissions{Deny: []string{"Read(.env)"}}
	for _, cmd := range []string{"echo x >| .env", "echo x 2>| .env", "date >|.env"} {
		if bashVerdict(p, cmd) != Deny {
			t.Errorf("%q was not denied", cmd)
		}
	}
	segs, _ := BashSegments("echo a >| f")
	if len(segs) != 1 {
		t.Errorf(`BashSegments("echo a >| f") = %q, want one segment`, segs)
	}
	segs, _ = BashSegments("ls | grep x || true")
	if strings.Join(segs, "\x00") != "ls\x00grep x\x00true" {
		t.Errorf("plain pipes changed: %q", segs)
	}
}

// TestReview_CdTracking (MED 4): cd options, pushd and popd.
func TestReview_CdTracking(t *testing.T) {
	newPathFixture(t)
	p := Permissions{Deny: []string{"Read(/sub/secret.txt)"}}
	for _, cmd := range []string{
		"cd -P sub && cat secret.txt",
		"cd -L sub; cat secret.txt",
		"cd -- sub && cat secret.txt",
		"pushd sub && cat secret.txt",
		"pushd -n sub >/dev/null; cat secret.txt",
	} {
		if got := bashVerdict(p, cmd); got != Deny {
			t.Errorf("%q = %v, want deny", cmd, got)
		}
	}
	for _, cmd := range []string{"popd && cat secret.txt", "cd - && cat secret.txt", "cd $DIR && cat secret.txt", "pushd +1 && cat secret.txt"} {
		if got := bashVerdict(p, cmd); got != Ask {
			t.Errorf("%q = %v, want ask (directory unknown)", cmd, got)
		}
	}
	if got := bashVerdict(p, "popd && cat /etc/hosts"); got != Allow {
		t.Errorf("an absolute path after popd = %v, want allow", got)
	}
}

// TestReview_ShellGlobs (MED 5): a glob operand is checked against every
// file it matches.
func TestReview_ShellGlobs(t *testing.T) {
	f := newPathFixture(t)
	mkfile(t, f.p(".env"))
	mkfile(t, f.p("sub/.env"))
	p := Permissions{Deny: []string{"Read(.env)"}}
	for _, cmd := range []string{"cat .en?", "cat .e*", "cat [.]env", "cat .{env,x}", "cat */.env", "cat ./.e\"n\"v*"} {
		if got := bashVerdict(p, cmd); got != Deny {
			t.Errorf("%q = %v, want deny", cmd, got)
		}
	}
	if got := bashVerdict(p, "cat '.e*'"); got == Deny {
		t.Error("a quoted '*' was expanded as a glob")
	}
	if got := bashVerdict(p, "cd - && cat *"); got != Ask {
		t.Errorf("a glob in an unknown directory = %v, want ask", got)
	}
}

// TestReview_Wrappers (MED 6): wrapper commands and their option values.
func TestReview_Wrappers(t *testing.T) {
	newPathFixture(t)
	read := Permissions{Deny: []string{"Read(.env)"}}
	for _, cmd := range []string{
		"env -i FOO=1 cat .env",
		"env -u HOME cat .env",
		"env -S 'cat .env'",
		"timeout 5 cat .env",
		"timeout -s KILL -k 2 5 cat .env",
		"nice -n 5 cat .env",
		"stdbuf -oL cat .env",
		"stdbuf -o L cat .env",
		"nohup cat .env",
		"time cat .env",
		"time -o .env ls",
		"command cat .env",
		"exec -a x cat .env",
		"xargs -n 1 cat .env",
		"xargs -a .env echo",
		"sudo -u root cat .env",
		"sudo -E -u root -- cat .env",
		"sudo -e .env",
		"FOO=1 BAR=2 cat .env",
		"dd if=.env of=/dev/null",
		"if true; then cat .env; fi",
	} {
		if got := bashVerdict(read, cmd); got != Deny {
			t.Errorf("%q = %v, want deny", cmd, got)
		}
	}
	edit := Permissions{Deny: []string{"Edit(~/.ssh/**)"}}
	if got := bashVerdict(edit, "dd if=/dev/zero of=~/.ssh/authorized_keys"); got != Deny {
		t.Errorf("dd of= = %v, want deny", got)
	}
	anchored := Permissions{Deny: []string{"Read(/sub/secret.txt)"}}
	if got := bashVerdict(anchored, "env -C sub cat secret.txt"); got != Deny {
		t.Errorf("env -C = %v, want deny", got)
	}
	if got := bashVerdict(anchored, "sudo -D sub cat secret.txt"); got != Deny {
		t.Errorf("sudo -D = %v, want deny", got)
	}
}

// TestReview_Substitutions (MED 7): command and process substitution
// bodies are commands too; a substitution used as a file operand is
// unknowable and asks.
func TestReview_Substitutions(t *testing.T) {
	newPathFixture(t)
	p := Permissions{Deny: []string{"Read(.env)"}}
	for _, cmd := range []string{
		"echo $(cat .env)",
		"x=$(cat .env)",
		"echo `cat .env`",
		`echo "$(cat .env)"`,
		"diff <(cat .env) b.txt",
		"echo $(echo $(cat .env))",
		"echo $(cat .env",
	} {
		if got := bashVerdict(p, cmd); got != Deny {
			t.Errorf("%q = %v, want deny", cmd, got)
		}
	}
	for _, cmd := range []string{"cat $(echo .env)", "cat `ls`", "cat $F", `cat "${F}"`, "cat ~root/x"} {
		if got := bashVerdict(p, cmd); got != Ask {
			t.Errorf("%q = %v, want ask (operand unknowable)", cmd, got)
		}
	}
	if got := bashVerdict(p, "cat '$F'"); got != Allow {
		t.Errorf("a single-quoted $ = %v, want allow (it is a literal file name)", got)
	}
	if got := bashVerdict(Permissions{}, "cat $F"); got != Allow {
		t.Errorf("with no path rules, an unknowable operand = %v, want allow", got)
	}
}

// TestReview_AskBeatsAllow (MED 8): Claude Code evaluates deny, then ask,
// then allow; a matching ask rule prompts even when an allow rule matches.
func TestReview_AskBeatsAllow(t *testing.T) {
	f := newPathFixture(t)
	cases := []struct {
		name, tool, arg string
		p               Permissions
	}{
		{"bash", "bash", "git push origin main", Permissions{Allow: []string{"Bash(git *)"}, Ask: []string{"Bash(git push *)"}}},
		{"bare tool", "write", f.p("a"), Permissions{Allow: []string{"Write"}, Ask: []string{"Write"}}},
		{"path", "edit", f.p("src/secret.go"), Permissions{Allow: []string{"Edit(src/**)"}, Ask: []string{"Edit(src/secret.go)"}}},
		{"web fetch", "web_fetch", "https://go.dev/x", Permissions{Allow: []string{"WebFetch"}, Ask: []string{"WebFetch(domain:go.dev)"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Decide(c.p, c.tool, c.arg, ModeManual); got != Ask {
				t.Errorf("got %v, want ask", got)
			}
		})
	}
}

// TestReview_ReadVariants (LOW 11): the read tool falls back to a
// curly-apostrophe spelling; the rule must judge what it opens. Deny rules
// also compare in NFC.
func TestReview_ReadVariants(t *testing.T) {
	f := newPathFixture(t)
	curly := "it’s.txt"
	mkfile(t, f.p(curly))
	if !denies("Read("+curly+")", "read", f.p("it's.txt")) {
		t.Error("read it's.txt (opens it’s.txt) was not denied by Read(it’s.txt)")
	}
	nfc, nfd := "café.txt", "café.txt"
	if !denies("Read("+nfc+")", "read", f.p(nfd)) {
		t.Error("an NFD path was not denied by the NFC rule")
	}
	if !denies("Read("+nfd+")", "read", f.p(nfc)) {
		t.Error("an NFC path was not denied by the NFD rule")
	}
}

// TestReview_EmptyParens (LOW 12): "Read()" in deny is the bare tool rule.
func TestReview_EmptyParens(t *testing.T) {
	f := newPathFixture(t)
	if !denies("Read()", "read", f.p("anything")) {
		t.Error("deny Read() did not deny reading")
	}
	if !asks("Edit()", "write", f.p("anything")) {
		t.Error("ask Edit() did not ask for write")
	}
	if allows("Edit()", "edit", f.p("anything")) {
		t.Error("allow Edit() approved an edit")
	}
}
