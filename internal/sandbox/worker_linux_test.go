//go:build linux

package sandbox

// Live tests of the Linux worker sandbox (bubblewrap with a private root and
// a PID namespace). They skip where bwrap or socat is missing or cannot
// create a user namespace.

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/execenv"
)

type workerRig struct {
	t             *testing.T
	base          string // short, for Unix socket paths
	parent, child string
	m             *Manager
	env           *execenv.Env
}

func newWorkerRig(t *testing.T) *workerRig {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	base, err := os.MkdirTemp("/tmp", "kw-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	t.Setenv("KILN_TMPDIR", filepath.Join(base, "t"))
	r := &workerRig{t: t, base: base, parent: filepath.Join(base, "parent"), child: filepath.Join(base, "child")}
	home := filepath.Join(base, "home")
	for _, d := range []string{r.parent, home} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "HOME="+home, "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git(r.parent, "init", "-q", "-b", "main")
	git(r.parent, "config", "user.name", "Test")
	git(r.parent, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(r.parent, "tracked"), []byte("parent"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(r.parent, "add", "tracked")
	git(r.parent, "commit", "-qm", "initial")
	git(r.parent, "worktree", "add", "-qb", "worker", r.child)

	m, err := NewWorker(Config{}, Options{Home: home}, r.parent, r.child)
	if err != nil {
		if strings.Contains(err.Error(), "unavailable") {
			t.Skipf("no Linux sandbox here: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	r.m = m
	r.env = execenv.New(r.child)
	return r
}

func (r *workerRig) run(command string) (string, int) {
	r.t.Helper()
	res, err := r.env.Exec(context.Background(), command, execenv.ExecOptions{
		InheritEnv: true, Timeout: 30 * time.Second, Sandbox: r.m.ForCommand(command, false),
	})
	if err != nil {
		r.t.Fatalf("exec %q: %v", command, err)
	}
	if strings.Contains(res.Text, "bwrap: ") {
		r.t.Fatalf("bwrap failed for %q: %s", command, res.Text)
	}
	return res.Text, res.ExitCode
}

// hostProcessWith reports whether any process on the host has marker in its
// command line.
func hostProcessWith(marker string) bool {
	dirs, _ := filepath.Glob("/proc/[0-9]*/cmdline")
	for _, f := range dirs {
		b, err := os.ReadFile(f)
		if err == nil && strings.Contains(string(b), marker) {
			return true
		}
	}
	return false
}

func waitHostProcess(t *testing.T, marker string, want bool, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if hostProcessWith(marker) == want {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A worker writes only its worktree and private temp, cannot write the parent
// checkout or the shared Git metadata, and cannot see the host's other files.
func TestWorkerLinuxFilesystem(t *testing.T) {
	r := newWorkerRig(t)
	if out, code := r.run(`touch child-file && touch "$TMPDIR/temp-file"`); code != 0 {
		t.Fatalf("worker write failed (%d): %s", code, out)
	}
	for _, command := range []string{
		"touch " + filepath.Join(r.parent, "parent-file"),
		"touch " + filepath.Join(r.parent, ".git", "worker-owned"),
		"touch " + filepath.Join(r.parent, ".git", "worktrees", "child", "index"),
		"touch .git",
		"rm .git",
		"touch " + filepath.Join(r.base, "escaped"),
		"cat " + filepath.Join(r.parent, "tracked"),
		"touch /etc/escaped",
	} {
		if out, code := r.run(command); code == 0 {
			t.Errorf("worker escape succeeded: %s (%s)", command, out)
		}
	}
	for _, p := range []string{
		filepath.Join(r.parent, "parent-file"), filepath.Join(r.parent, ".git", "worker-owned"),
		filepath.Join(r.base, "escaped"),
	} {
		mustNotExist(t, p)
	}
	// Git still works against the read-only metadata.
	if out, code := r.run("git status --porcelain"); code != 0 {
		t.Errorf("git status in worker failed (%d): %s", code, out)
	}
	// An inode shared with the parent is refused rather than written through.
	if err := os.Link(filepath.Join(r.parent, "tracked"), filepath.Join(r.child, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.ForCommand("cat alias", false).Wrap("/bin/sh", "cat alias", r.child); err == nil {
		t.Error("worker accepted a hardlink alias to a parent file")
	}
}

// Path-based Unix sockets of the host are unreachable: a socket the ordinary
// sandbox still lets a command connect to does not exist for a worker, and a
// socket inside the worktree's Git metadata (fsmonitor) is covered.
func TestWorkerLinuxHidesHostUnixSockets(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	r := newWorkerRig(t)
	serve := func(path string) {
		ln, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("reached")) })}
		go srv.Serve(ln)
		t.Cleanup(func() { srv.Close() })
	}
	tmpSock := filepath.Join(r.base, "host.sock")
	gitSock := filepath.Join(r.parent, ".git", "fsmonitor--daemon.ipc")
	serve(tmpSock)
	serve(gitSock)
	probe := func(sock string) string {
		return `curl -s -m 5 --unix-socket ` + sock + ` http://x/ ; echo " exit=$?"`
	}

	// Control: an ordinary sandbox reaches the same sockets, so the probe can fail.
	ordinary := newRealRig(t, Config{}, nil)
	if out, _ := ordinary.run(probe(tmpSock)); !strings.Contains(out, "reached") {
		t.Fatalf("control: ordinary sandbox cannot reach the test socket: %s", out)
	}
	for _, sock := range []string{tmpSock, gitSock} {
		if out, _ := r.run(probe(sock)); strings.Contains(out, "reached") {
			t.Errorf("worker reached host socket %s: %s", sock, out)
		}
	}
}

func TestWorkerLinuxNetworkIsolated(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	r := newWorkerRig(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("reached")) })}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	if out, _ := r.run(`curl -s -m 5 http://` + ln.Addr().String() + `/ ; echo " exit=$?"`); strings.Contains(out, "reached") {
		t.Errorf("worker reached a host loopback listener: %s", out)
	}
}

// A descendant that double-forks and calls setsid() leaves the command's
// process group, yet does not outlive the command: its PID namespace ends
// with the command's init.
func TestWorkerLinuxDetachedDescendantDiesWithCommand(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid not installed")
	}
	r := newWorkerRig(t)
	const marker = "4242.0101"
	out, code := r.run(`(setsid sleep ` + marker + ` </dev/null >/dev/null 2>&1 &) ; sleep 0.3; echo started`)
	if code != 0 || !strings.Contains(out, "started") {
		t.Fatalf("command failed (%d): %s", code, out)
	}
	if !waitHostProcess(t, marker, false, 2*time.Second) {
		t.Fatal("a setsid descendant outlived the command")
	}
}

// Closing the worker's job scope while a command runs stops its detached
// descendants too, before the caller removes the worktree.
func TestWorkerLinuxDetachedDescendantDiesWithScope(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid not installed")
	}
	r := newWorkerRig(t)
	r.env.JobScope = execenv.NewJobScope()
	const marker = "4242.0202"
	command := `(setsid sleep ` + marker + ` </dev/null >/dev/null 2>&1 &) ; sleep 300`
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = r.env.Exec(context.Background(), command, execenv.ExecOptions{
			InheritEnv: true, Timeout: 60 * time.Second, Sandbox: r.m.ForCommand(command, false),
		})
	}()
	if !waitHostProcess(t, marker, true, 10*time.Second) {
		r.env.JobScope.Close()
		t.Fatal("descendant never started")
	}
	r.env.JobScope.Close()
	<-done
	if !waitHostProcess(t, marker, false, 2*time.Second) {
		t.Fatal("a setsid descendant outlived JobScope.Close")
	}
}
