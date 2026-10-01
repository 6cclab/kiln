package settings

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

// Regression tests for the final verification of 6f31a5c. They use only
// Decide and the test helpers that commit already had, so each can be run
// against it.

// TestRound4_UnicodeCaseFolding (HIGH 1): deny rules fold case the way
// Unicode does, not as strings.ToLower: µ/μ, ς/σ, ſ/s.
func TestRound4_UnicodeCaseFolding(t *testing.T) {
	if !foldCase {
		t.Skip("case folding is for case-insensitive filesystems")
	}
	f := newPathFixture(t)
	cases := []struct{ rule, path string }{
		{"Read(μ.txt)", "µ.txt"},          // μ rule, µ (micro sign) path
		{"Read(σ.txt)", "ς.txt"},          // σ rule, ς path
		{"Read(secrets/**)", "ſecrets/K"}, // ſ (long s)
		{"Read(βeta.txt)", "ϐeta.txt"},    // β rule, ϐ path
	}
	for _, c := range cases {
		if !denies(c.rule, "read", f.p(c.path)) {
			t.Errorf("%s did not deny %q", c.rule, c.path)
		}
	}
}

// TestRound4_LongSOnDisk (HIGH 1): on APFS, "ſecrets/K" opens
// "Secrets/k"; the gate must see that file.
func TestRound4_LongSOnDisk(t *testing.T) {
	f := newPathFixture(t)
	mkfile(t, f.p("Secrets/k"))
	alias := f.p("ſecrets/K")
	if _, err := os.Stat(alias); err != nil {
		t.Skip("this filesystem does not fold ſ")
	}
	if !denies("Read(/Secrets/**)", "read", alias) {
		t.Errorf("Read(/Secrets/**) did not deny %s", alias)
	}
	if got := bashVerdict(Permissions{Deny: []string{"Read(/Secrets/**)"}}, "cat ſecrets/K"); got != Deny {
		t.Errorf("bash cat ſecrets/K = %v, want deny", got)
	}
}

// TestRound4_Firmlink (HIGH 2): /System/Volumes/Data/<path> is <path>.
func TestRound4_Firmlink(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	f := newPathFixture(t)
	mkfile(t, f.h("secrets/k"))
	real, err := filepath.EvalSymlinks(f.h("secrets/k"))
	if err != nil {
		t.Fatal(err)
	}
	alias := "/System/Volumes/Data" + real
	if _, err := os.Stat(alias); err != nil {
		t.Skip("no firmlink here")
	}
	p := Permissions{Deny: []string{"Read(~/secrets/**)"}}
	if Decide(p, "read", alias, ModeAuto) != Deny {
		t.Errorf("read %s was not denied by Read(~/secrets/**)", alias)
	}
	if got := bashVerdict(p, "cat "+alias); got != Deny {
		t.Errorf("bash cat %s = %v, want deny", alias, got)
	}
	// Sanity: the firmlink spelling of a file outside ~/secrets is not
	// denied, so the rule is not simply matching everything.
	outside := "/System/Volumes/Data" + filepath.Join(filepath.Dir(filepath.Dir(real)), "public", "x")
	if Decide(p, "read", outside, ModeAuto) == Deny {
		t.Errorf("denied %s, outside ~/secrets", outside)
	}
}

// TestRound4_VolInodePath (HIGH 3): /.vol/<dev>/<inode> is the file it
// names; and a bash operand under /.vol that no rule matches is unsure.
func TestRound4_VolInodePath(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	f := newPathFixture(t)
	mkfile(t, f.h("secrets/k"))
	var st syscall.Stat_t
	if err := syscall.Stat(f.h("secrets/k"), &st); err != nil {
		t.Fatal(err)
	}
	alias := fmt.Sprintf("/.vol/%d/%d", st.Dev, st.Ino)
	if _, err := os.Stat(alias); err != nil {
		t.Skip("no /.vol here")
	}
	p := Permissions{Deny: []string{"Read(~/secrets/**)"}}
	if Decide(p, "read", alias, ModeAuto) != Deny {
		t.Errorf("read %s was not denied", alias)
	}
	if got := bashVerdict(p, "cat "+alias); got != Deny {
		t.Errorf("bash cat %s = %v, want deny", alias, got)
	}
	other := Permissions{Deny: []string{"Read(.env)"}}
	if got := bashVerdict(other, "cat "+alias); got != Ask {
		t.Errorf("bash cat of a /.vol path = %v, want ask", got)
	}
}

// TestRound4_LineContinuation (MED 4): backslash-newline is removed before
// words are split.
func TestRound4_LineContinuation(t *testing.T) {
	newPathFixture(t)
	p := Permissions{Deny: []string{"Read(.env)"}}
	for _, cmd := range []string{"cat \\\n.env", "c\\\nat .env", "cat .e\\\nnv", "cat \".e\\\nnv\""} {
		if got := bashVerdict(p, cmd); got != Deny {
			t.Errorf("%q = %v, want deny", cmd, got)
		}
	}
	if got := bashVerdict(p, "cat '.e\\\nnv'"); got != Allow {
		t.Errorf("a backslash-newline inside single quotes = %v, want allow (it is text)", got)
	}
	if got := bashVerdict(Permissions{Deny: []string{"Bash(rm *)"}}, "rm \\\n-rf x"); got != Deny {
		t.Errorf("Bash(rm *) on rm \\<NL>-rf = %v, want deny", got)
	}
}

// TestRound4_ShellStdin (MED 5): a shell reading its script from a heredoc
// or herestring runs it; from a pipe, what it runs is unknowable.
func TestRound4_ShellStdin(t *testing.T) {
	newPathFixture(t)
	p := Permissions{Deny: []string{"Read(.env)"}}
	for _, cmd := range []string{
		"bash <<EOF\ncat .env\nEOF",
		"bash -s <<EOF\ncat .env\nEOF",
		"sh <<'EOF'\ncat .env\nEOF",
		"bash -s arg <<EOF\ncat .env\nEOF",
		"bash <<< 'cat .env'",
		"zsh <<<\"head .env\"",
	} {
		if got := bashVerdict(p, cmd); got != Deny {
			t.Errorf("%q = %v, want deny", cmd, got)
		}
	}
	for _, cmd := range []string{"echo 'cat .env' | sh", "curl x | bash", "bash"} {
		if got := bashVerdict(p, cmd); got != Ask {
			t.Errorf("%q = %v, want ask", cmd, got)
		}
	}
	for _, cmd := range []string{"bash < script.sh", "cat <<EOF\ncat .env\nEOF", "bash -c 'echo hi'"} {
		if got := bashVerdict(p, cmd); got != Allow {
			t.Errorf("%q = %v, want allow", cmd, got)
		}
	}
}
