//go:build linux

package sandbox

// Live tests of the Linux worker sandbox (bubblewrap with a private root and
// a PID namespace). They skip where bwrap or socat is missing or cannot
// create a user namespace.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/execenv"
	"golang.org/x/sys/unix"
)

type workerRig struct {
	t             *testing.T
	base          string // short, for Unix socket paths
	parent, child string
	home          string
	m             *Manager
	env           *execenv.Env
}

// newWorkerRepo makes a parent repository and a linked worktree, no sandbox.
func newWorkerRepo(t *testing.T) *workerRig {
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
	r.home = home
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
	return r
}

func newWorkerRig(t *testing.T) *workerRig {
	t.Helper()
	r := newWorkerRepo(t)
	m, err := NewWorker(Config{}, Options{Home: r.home}, r.parent, r.child)
	if err != nil {
		if strings.Contains(err.Error(), "unavailable") {
			t.Skipf("no Linux sandbox here: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Shutdown() })
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
	// comm survives while a process tears down its memory; cmdline does not.
	for _, name := range []string{"cmdline", "comm"} {
		files, _ := filepath.Glob("/proc/[0-9]*/" + name)
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err == nil && strings.Contains(string(b), marker) && !isZombie(filepath.Dir(f)) {
				return true
			}
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
// with the command's init, and Exec returns only once the namespace is empty.
// Each check is a single look right after Exec returns, with no polling, and
// runs many times because a race would show up as an occasional survivor.
func TestWorkerLinuxDetachedDescendantDiesWithCommand(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid not installed")
	}
	r := newWorkerRig(t)
	for i := 0; i < 20; i++ {
		marker := fmt.Sprintf("4242.01%02d", i)
		out, code := r.run(`(setsid sleep ` + marker + ` </dev/null >/dev/null 2>&1 &) ; sleep 0.1; echo started`)
		if code != 0 || !strings.Contains(out, "started") {
			t.Fatalf("command failed (%d): %s", code, out)
		}
		if hostProcessWith(marker) {
			t.Fatalf("run %d: a setsid descendant outlived the command", i)
		}
	}
}

// Closing the worker's job scope while a command runs stops its detached
// descendants too, and Close returns only after they are gone: one look, no
// polling, 20 times.
func TestWorkerLinuxDetachedDescendantDiesWithScope(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid not installed")
	}
	r := newWorkerRig(t)
	for i := 0; i < 20; i++ {
		r.env.JobScope = execenv.NewJobScope()
		marker := fmt.Sprintf("4242.02%02d", i)
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
		if hostProcessWith(marker) {
			t.Fatalf("run %d: a setsid descendant outlived JobScope.Close", i)
		}
		<-done
	}
}

// If the namespace is not confirmed empty, Shutdown says so (the caller then
// keeps the worktree). The injected opener hands back a pidfd for a process
// that never exits; the reported child-pid is 1, so the init check passes.
func TestNSWatchTimeoutIsAnError(t *testing.T) {
	sleeper := exec.Command("sleep", "60")
	if err := sleeper.Start(); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })
	m := New(Config{}, Options{
		NSWaitTimeout: 150 * time.Millisecond,
		PidfdOpen:     func(int) (int, error) { return unix.PidfdOpen(sleeper.Process.Pid, 0) },
		// An init the kill does not stop in time.
		PidfdSignal: func(int, syscall.Signal) error { return nil },
	})
	n, err := m.newNSWatch()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.w.WriteString(`{"child-pid": 1}`); err != nil {
		t.Fatal(err)
	}
	n.start()
	begin := time.Now()
	err = n.drain(nil)
	if err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("drain = %v, want a still-running error", err)
	}
	if d := time.Since(begin); d < 100*time.Millisecond || d > 5*time.Second {
		t.Errorf("drain returned after %s, want about the 150ms timeout", d)
	}
	n.finish() // as Exec's cleanup does: the unconfirmed watch stays registered
	if err := m.Shutdown(); err == nil {
		t.Fatal("Shutdown reported success while a namespace was unconfirmed")
	}
}

// bwrap killed before it reported its namespace may have left one unseen: that
// is an error. bwrap that exited on its own before making one is not.
func TestNSWatchNoInfo(t *testing.T) {
	states := map[string]*os.ProcessState{}
	for name, script := range map[string]string{"killed": "kill -9 $$", "exited": "exit 1"} {
		c := exec.Command("sh", "-c", script)
		_ = c.Run()
		states[name] = c.ProcessState
	}
	for name, wantErr := range map[string]bool{"killed": true, "exited": false} {
		m := New(Config{}, Options{NSWaitTimeout: time.Second})
		n, err := m.newNSWatch()
		if err != nil {
			t.Fatal(err)
		}
		_ = n.w.Close() // bwrap ended without writing anything
		n.start()
		err = n.stopped(states[name], time.Second)
		if (err != nil) != wantErr {
			t.Errorf("%s: err = %v, want error: %v", name, err, wantErr)
		}
	}
}

// Without pidfd_open, or a bwrap that does not report its namespace, worker
// shell is refused instead of falling back to teardown nobody waits for.
func TestWorkerLinuxRefusedWithoutPidfd(t *testing.T) {
	r := newWorkerRepo(t)
	_, err := NewWorker(Config{}, Options{Home: r.home, PidfdOpen: func(int) (int, error) { return -1, unix.ENOSYS }}, r.parent, r.child)
	if err == nil {
		t.Fatal("worker sandbox created without pidfd support")
	}
	if strings.Contains(err.Error(), "unavailable") {
		t.Skipf("no Linux sandbox here: %v", err)
	}
	if !strings.Contains(err.Error(), "pidfd") {
		t.Fatalf("error does not name pidfd: %v", err)
	}
}

// Teardown can be slow: a descendant holding a lot of memory takes a while to
// exit after SIGKILL. JobScope.Close must still return only after it is gone,
// which is only true because Exec waits for the namespace to empty (without
// that wait this test fails: the kernel finishes after Close has returned).
func TestWorkerLinuxScopeCloseWaitsForSlowTeardown(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid not installed")
	}
	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("awk not installed")
	}
	if avail := memAvailableKB(); avail < 4_000_000 {
		t.Skipf("needs about 2 GB free, have %d kB", avail)
	}
	r := newWorkerRig(t)
	r.env.JobScope = execenv.NewJobScope()
	const marker = "zslow4242" // the copied awk's process name
	command := `cp "$(command -v awk)" ./` + marker + ` && (head -c 900000000 /dev/zero | setsid ./` + marker +
		` '{l=$0} END{system("sleep 300")}' >/dev/null 2>&1 &) ; sleep 300`
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = r.env.Exec(context.Background(), command, execenv.ExecOptions{
			InheritEnv: true, Timeout: 120 * time.Second, Sandbox: r.m.ForCommand(command, false),
		})
	}()
	if !waitHostProcess(t, marker, true, 20*time.Second) {
		r.env.JobScope.Close()
		t.Fatal("descendant never started")
	}
	time.Sleep(4 * time.Second) // let it read the input and grow
	begin := time.Now()
	r.env.JobScope.Close()
	t.Logf("Close took %s", time.Since(begin))
	if hostProcessWith(marker) {
		t.Fatal("a memory-heavy setsid descendant outlived JobScope.Close")
	}
	<-done
}

func memAvailableKB() int {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "MemAvailable:" {
			n, _ := strconv.Atoi(f[1])
			return n
		}
	}
	return 0
}

// isZombie reports whether the /proc/<pid> directory is a process that has
// exited and only awaits its parent's wait (it runs nothing).
func isZombie(dir string) bool {
	b, err := os.ReadFile(dir + "/stat")
	if err != nil {
		return true // gone
	}
	s := string(b)
	i := strings.LastIndex(s, ")")
	return i >= 0 && i+2 < len(s) && s[i+2] == 'Z'
}

// The worker's IPC namespace is not the host's.
func TestWorkerLinuxIPCNamespaceIsolated(t *testing.T) {
	host, err := os.Readlink("/proc/self/ns/ipc")
	if err != nil {
		t.Skip(err)
	}
	r := newWorkerRig(t)
	out, code := r.run(`readlink /proc/self/ns/ipc`)
	if code != 0 || !strings.HasPrefix(strings.TrimSpace(out), "ipc:[") {
		t.Fatalf("readlink in worker: exit %d: %s", code, out)
	}
	if strings.TrimSpace(out) == host {
		t.Errorf("worker shares the host IPC namespace %s", host)
	}
}

// A namespace init that outlived bwrap (killed before its init armed
// PR_SET_PDEATHSIG) is killed by teardown, not merely waited on.
func TestNSWatchKillsInitThatOutlivedBwrap(t *testing.T) {
	orphan := exec.Command("sleep", "60")
	if err := orphan.Start(); err != nil {
		t.Skip(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- orphan.Wait() }()
	t.Cleanup(func() { _ = orphan.Process.Kill() })
	m := New(Config{}, Options{
		NSWaitTimeout: 2 * time.Second,
		PidfdOpen:     func(int) (int, error) { return unix.PidfdOpen(orphan.Process.Pid, 0) },
	})
	n, err := m.newNSWatch()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.w.WriteString(`{"child-pid": 1}`); err != nil {
		t.Fatal(err)
	}
	n.start()
	if err := n.drain(nil); err != nil {
		t.Fatalf("drain = %v, want the orphaned init killed and confirmed gone", err)
	}
	n.finish()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("orphaned init still running after drain")
	}
}

// Exec's cleanup and Manager.Shutdown may release the same watch at once
// (Shutdown runs when JobScope.Close returns, which is before Exec's cleanup).
// Run under -race.
func TestNSWatchConcurrentReleaseAndShutdown(t *testing.T) {
	for i := 0; i < 50; i++ {
		done := exec.Command("true")
		if err := done.Start(); err != nil {
			t.Skip(err)
		}
		pid := done.Process.Pid
		fd, err := unix.PidfdOpen(pid, 0)
		if err != nil {
			t.Skip(err)
		}
		_ = done.Wait()
		m := New(Config{}, Options{
			NSWaitTimeout: time.Second,
			PidfdOpen:     func(int) (int, error) { return fd, nil },
		})
		n, err := m.newNSWatch()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := n.w.WriteString(`{"child-pid": 1}`); err != nil {
			t.Fatal(err)
		}
		n.start()
		<-n.done
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _ = n.drain(nil); n.finish() }()
		go func() { defer wg.Done(); _ = m.Shutdown() }()
		wg.Wait()
	}
}

func TestIsNSInit(t *testing.T) {
	for status, want := range map[string]bool{
		"Name:\tsh\nNSpid:\t4242\t1\n":      true,
		"Name:\tsh\nNSpid:\t12345\t12345\n": false,
		"Name:\tsh\nNSpid:\t4242\t17\n":     false,
		"Name:\tsh\n":                       true, // no NSpid line: cannot check
	} {
		if got := isNSInit(status); got != want {
			t.Errorf("isNSInit(%q) = %v, want %v", status, got, want)
		}
	}
}

// A worker whose namespace Shutdown cannot confirm empty keeps its private
// temp directory too: a process still running may be writing there.
func TestWorkerShutdownKeepsTempWhenUnconfirmed(t *testing.T) {
	r := newWorkerRig(t)
	sleeper := exec.Command("sleep", "60")
	if err := sleeper.Start(); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })
	r.m.opts.NSWaitTimeout = 150 * time.Millisecond
	r.m.opts.PidfdOpen = func(int) (int, error) { return unix.PidfdOpen(sleeper.Process.Pid, 0) }
	r.m.opts.PidfdSignal = func(int, syscall.Signal) error { return nil }
	n, err := r.m.newNSWatch()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.w.WriteString(`{"child-pid": 1}`); err != nil {
		t.Fatal(err)
	}
	n.start()
	_ = n.drain(nil)
	n.finish()
	if err := r.m.Shutdown(); err == nil {
		t.Fatal("Shutdown reported success while a namespace was unconfirmed")
	}
	if _, err := os.Stat(r.m.worker.temp); err != nil {
		t.Errorf("worker temp removed after an unconfirmed shutdown: %v", err)
	}
}
