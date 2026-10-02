package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/andrepato/harness/internal/sandbox"
)

// kiln's status-line git runs inside the session's sandbox: a clean
// filter the repository selects (planted through .gitattributes, defined
// in config) runs, but sandboxed, so it cannot write outside the
// workspace. Without the sandbox the same filter writes its marker, which
// shows the fixture really makes git status run it.
func TestKilnGitRunsInSandbox(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("needs sandbox-exec")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	scratch, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(scratch, "ws")
	outside := filepath.Join(scratch, "outside")
	os.MkdirAll(repo, 0o755)
	os.MkdirAll(outside, 0o755)
	marker := filepath.Join(outside, "filter-ran")
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644)
	os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("a.txt filter=x\n"), 0o644)
	git("add", "a.txt", ".gitattributes")
	git("-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-qm", "first")
	git("config", "filter.x.clean", "touch "+marker+"; cat")
	dirty := func() { os.WriteFile(filepath.Join(repo, "a.txt"), []byte("two\n"), 0o644) } // same size: git must hash it

	tmp := filepath.Join(scratch, "tmp")
	os.Mkdir(tmp, 0o700)
	m := sandbox.New(sandbox.Config{Enabled: true}, sandbox.Options{Cwd: repo, TmpDir: tmp, Home: filepath.Join(scratch, "home")})
	defer m.Close()
	setGitSandbox(m.Always())
	defer setGitSandbox(nil)

	dirty()
	st, ok := readGitStatusAt(context.Background(), repo)
	if !ok || !st.Dirty {
		t.Fatalf("sandboxed git status: ok=%v %+v", ok, st)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the repository's filter wrote outside the workspace from kiln's git status")
	}

	setGitSandbox(nil)
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("six\n"), 0o644)
	readGitStatusAt(context.Background(), repo)
	if _, err := os.Stat(marker); err != nil {
		t.Skip("this git did not run the clean filter on status; the check above proves nothing here")
	}
}
