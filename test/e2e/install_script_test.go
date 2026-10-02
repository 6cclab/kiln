//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoRootDir is the kiln checkout this test package lives in.
func repoRootDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// runInstall runs scripts/install.sh through sh with args, with TMPDIR
// pointed at a fresh directory so the test can check the script cleans up.
func runInstall(t *testing.T, tmpdir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{filepath.Join(repoRootDir(t), "scripts", "install.sh")}, args...)...)
	cmd.Env = append(os.Environ(), "TMPDIR="+tmpdir)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func kilnVersion(t *testing.T, bin string) string {
	t.Helper()
	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("%s --version: %v\n%s", bin, err, out)
	}
	return strings.TrimSpace(string(out))
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("install.sh left %d entries in its temp dir, e.g. %s", len(entries), entries[0].Name())
	}
}

// TestInstallScript_FromLocalSource builds this checkout with --source and
// installs a working binary, leaving no temp files behind.
func TestInstallScript_FromLocalSource(t *testing.T) {
	dir, tmp := t.TempDir(), t.TempDir()
	out, err := runInstall(t, tmp, "--source", repoRootDir(t), "--dir", dir)
	if err != nil {
		t.Fatalf("install.sh: %v\n%s", err, out)
	}
	if v := kilnVersion(t, filepath.Join(dir, "kiln")); !strings.HasPrefix(v, "kiln ") {
		t.Errorf("--version = %q", v)
	}
	assertEmptyDir(t, tmp)
}

// TestInstallScript_ClonesARef clones a repository at a branch and at a
// bare commit. The clone and the built binary once shared one path, so the
// clone path failed; this covers it.
func TestInstallScript_ClonesARef(t *testing.T) {
	root := repoRootDir(t)
	repo := filepath.Join(t.TempDir(), "repo.git")
	if out, err := exec.Command("git", "clone", "--quiet", "--bare", root, repo).CombinedOutput(); err != nil {
		t.Fatalf("git clone --bare: %v\n%s", err, out)
	}
	head, err := exec.Command("git", "-C", root, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	branch, err := exec.Command("git", "-C", root, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{strings.TrimSpace(string(branch)), strings.TrimSpace(string(head))} {
		t.Run(ref, func(t *testing.T) {
			dir, tmp := t.TempDir(), t.TempDir()
			out, err := runInstall(t, tmp, "--repo", "file://"+repo, "--ref", ref, "--dir", dir)
			if err != nil {
				t.Fatalf("install.sh: %v\n%s", err, out)
			}
			if v := kilnVersion(t, filepath.Join(dir, "kiln")); !strings.Contains(v, strings.TrimSpace(string(head))) {
				t.Errorf("--version = %q, want it to name %s", v, head)
			}
			assertEmptyDir(t, tmp)
		})
	}
}

// TestInstallScript_RefusesBadInput: an unknown ref and a directory that
// isn't a kiln checkout fail with a message and install nothing.
func TestInstallScript_RefusesBadInput(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo.git")
	if out, err := exec.Command("git", "clone", "--quiet", "--bare", repoRootDir(t), repo).CombinedOutput(); err != nil {
		t.Fatalf("git clone --bare: %v\n%s", err, out)
	}
	cases := map[string][]string{
		"unknown ref":    {"--repo", "file://" + repo, "--ref", "no-such-ref"},
		"not a checkout": {"--source", t.TempDir()},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			dir, tmp := t.TempDir(), t.TempDir()
			out, err := runInstall(t, tmp, append(args, "--dir", dir)...)
			if err == nil {
				t.Fatalf("install.sh succeeded, want failure\n%s", out)
			}
			if !strings.Contains(out, "kiln install:") {
				t.Errorf("no kiln install: message in output:\n%s", out)
			}
			if _, err := os.Stat(filepath.Join(dir, "kiln")); err == nil {
				t.Error("a kiln binary was installed despite the failure")
			}
			assertEmptyDir(t, tmp)
		})
	}
}
