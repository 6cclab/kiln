package execenv

import (
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRealPath(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := func(to, at string) {
		t.Helper()
		if err := os.Symlink(to, filepath.Join(dir, at)); err != nil {
			t.Fatal(err)
		}
	}
	link(filepath.Join(target, "missing.txt"), "dangle")
	link("target", "reldir")
	link("dangle", "hop")
	link("loopA", "loopB")
	link("loopB", "loopA")

	cases := []struct {
		in, want string
		ok       bool
	}{
		{filepath.Join(dir, "dangle"), filepath.Join(target, "missing.txt"), true},
		{filepath.Join(dir, "hop"), filepath.Join(target, "missing.txt"), true},
		{filepath.Join(dir, "reldir", "new", "file"), filepath.Join(target, "new", "file"), true},
		{filepath.Join(dir, "reldir", "..", "x"), filepath.Join(dir, "x"), true},
		{filepath.Join(dir, "plain"), filepath.Join(dir, "plain"), true},
		{filepath.Join(dir, "loopA", "x"), filepath.Join(dir, "loopA", "x"), false},
	}
	for _, c := range cases {
		got, ok := RealPath(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("RealPath(%s) = %s, %v; want %s, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// TestRealPath_DotDotInLinkTargets: ".." inside a link target is walked
// physically, from the link's real target, as the kernel walks it.
func TestRealPath_DotDotInLinkTargets(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	proj := filepath.Join(root, "proj")
	for _, d := range []string{filepath.Join(home, ".ssh"), filepath.Join(proj, "deep", "er")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := func(to, at string) {
		t.Helper()
		if err := os.Symlink(to, at); err != nil {
			t.Fatal(err)
		}
	}
	link(filepath.Join(home, ".ssh"), filepath.Join(proj, "sshl")) // absolute
	link("sshl/..", filepath.Join(proj, "evil"))                   // relative, ".." after a link
	link(proj+"/sshl/..", filepath.Join(proj, "abs"))              // absolute, unclean on purpose
	link("evil", filepath.Join(proj, "chain"))                     // chain onto it
	link("../../sshl/..", filepath.Join(proj, "deep", "er", "up")) // climbs, then through a link
	link("../../../../../../../../../../..", filepath.Join(proj, "top"))

	keys := filepath.Join(home, ".ssh", "authorized_keys")
	etc, err := filepath.EvalSymlinks("/etc") // /private/etc on macOS
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ in, want string }{
		{filepath.Join(proj, "evil", ".ssh", "authorized_keys"), keys},
		{filepath.Join(proj, "abs", ".ssh", "authorized_keys"), keys},
		{filepath.Join(proj, "chain", ".ssh", "authorized_keys"), keys},
		{filepath.Join(proj, "deep", "er", "up", ".ssh", "authorized_keys"), keys},
		// A path's own ".." after a link climbs out of the link's target.
		{proj + "/sshl/../.ssh/authorized_keys", keys},
		// ".." above the root stays at the root.
		{filepath.Join(proj, "top", "etc"), etc},
		// ".." out of a missing component is back on real paths: the
		// link after it is followed.
		{proj + "/nope/../sshl/authorized_keys", keys},
	}
	for _, c := range cases {
		got, ok := RealPath(c.in)
		if !ok || got != c.want {
			t.Errorf("RealPath(%s) = %s, %v; want %s", c.in, got, ok, c.want)
		}
	}
}

// TestRealPath_MatchesEvalSymlinks builds random trees of directories and
// links (relative, absolute, with ".." in their targets) and checks
// RealPath against filepath.EvalSymlinks for every path that exists.
func TestRealPath_MatchesEvalSymlinks(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	compared := 0
	defer func() {
		if compared < 300 {
			t.Errorf("only %d paths compared; the generator no longer exercises RealPath", compared)
		}
	}()
	for round := 0; round < 30; round++ {
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		var dirs []string
		for _, d := range []string{"a", "a/b", "a/b/c", "d", "d/e"} {
			p := filepath.Join(root, d)
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(p, "f"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			dirs = append(dirs, p)
		}
		targets := []string{"..", "../..", "b", "../d", "c/..", "../b/..", "e/../..", "f"}
		var links []string
		for i := 0; i < 6; i++ {
			at := filepath.Join(dirs[rng.Intn(len(dirs))], "l"+string(rune('0'+i)))
			to := targets[rng.Intn(len(targets))]
			if rng.Intn(3) == 0 {
				to = filepath.Join(dirs[rng.Intn(len(dirs))], to) // absolute
			}
			if os.Symlink(to, at) == nil {
				links = append(links, at)
			}
		}
		suffixes := []string{"", "/f", "/..", "/../f", "/b", "/b/../f", "/l0", "/l1/..", "/e/f"}
		for _, l := range append(links, dirs...) {
			for _, s := range suffixes {
				in := l + s
				want, err := filepath.EvalSymlinks(in)
				if err != nil {
					continue // does not exist, or a loop
				}
				// EvalSymlinks cleans its input first; compare only when
				// that gives the kernel's answer (no ".." after a link).
				if _, err := os.Stat(in); err != nil {
					continue
				}
				compared++
				got, ok := RealPath(in)
				kernel := kernelPath(t, in)
				if !ok || got != kernel {
					t.Errorf("round %d: RealPath(%s) = %s, %v; kernel %s (EvalSymlinks %s)", round, in, got, ok, kernel, want)
				}
			}
		}
	}
}

// kernelPath is where the kernel resolves in, asked of the kernel: a child
// process started in the directory (the kernel's chdir walks the path
// physically) runs "pwd -P". For a file, the same for its directory part
// (the text before the last "/", unclean), then the leaf, following it if
// it is itself a link.
func kernelPath(t *testing.T, in string) string {
	t.Helper()
	fi, err := os.Stat(in)
	if err != nil {
		t.Fatal(err)
	}
	if fi.IsDir() {
		return physicalDir(t, in)
	}
	i := strings.LastIndex(in, "/")
	dir, leaf := in[:i], in[i+1:]
	if dir == "" {
		dir = "/"
	}
	if lfi, err := os.Lstat(in); err == nil && lfi.Mode()&os.ModeSymlink != 0 {
		to, err := os.Readlink(in)
		if err != nil {
			t.Fatal(err)
		}
		if !filepath.IsAbs(to) {
			to = physicalDir(t, dir) + "/" + to
		}
		return kernelPath(t, to)
	}
	return filepath.Join(physicalDir(t, dir), leaf)
}

func physicalDir(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("/bin/pwd", "-P")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("pwd -P in %s: %v", dir, err)
	}
	return strings.TrimSpace(string(out))
}
