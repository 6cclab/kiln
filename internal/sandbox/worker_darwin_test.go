//go:build darwin

package sandbox

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/execenv"
)

func TestWorkerRealSeatbeltIsolation(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	base := t.TempDir()
	t.Setenv("KILN_TMPDIR", filepath.Join(base, "temp-base"))
	parent, child, home := filepath.Join(base, "parent"), filepath.Join(base, "child"), filepath.Join(base, "home")
	for _, d := range []string{parent, home} {
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
	git(parent, "init", "-q", "-b", "main")
	git(parent, "config", "user.name", "Test")
	git(parent, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(parent, "tracked"), []byte("parent"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(parent, "add", "tracked")
	git(parent, "commit", "-qm", "initial")
	git(parent, "worktree", "add", "-qb", "worker", child)
	if err := os.WriteFile(filepath.Join(parent, "private"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "private"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	otherTmp := filepath.Join(execenv.TempRoot(), "parent-temp")
	if err := os.MkdirAll(otherTmp, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := Config{DenyRead: []Rule{{Path: filepath.Join(parent, "private")}},
		AllowWrite: []Rule{{Path: base}}, Excluded: []string{"touch"},
		AllowUnsandboxed: true, AllowedDomains: []string{"example.com"}}
	m, err := NewWorker(cfg, Options{Home: home}, parent, child)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	if !m.WillSandbox("touch child-file", true) {
		t.Fatal("worker command escaped with disable flag or excludedCommands")
	}
	env := execenv.New(child)
	run := func(command string) (string, int) {
		t.Helper()
		res, err := env.Exec(context.Background(), command, execenv.ExecOptions{
			InheritEnv: true, Timeout: 10 * time.Second, Sandbox: m.ForCommand(command, false),
		})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(res.Text, "sandbox-exec:") {
			t.Fatalf("Seatbelt profile did not load: %s", res.Text)
		}
		return res.Text, res.ExitCode
	}
	if out, code := run("touch child-file && touch \"$TMPDIR/temp-file\""); code != 0 {
		t.Fatalf("worker write failed (%d): %s", code, out)
	}
	for _, command := range []string{
		"touch " + filepath.Join(parent, "parent-file"),
		"touch .git",
		"rm .git",
		"touch " + filepath.Join(parent, ".git", "worktrees", "child", "index"),
		"touch " + filepath.Join(otherTmp, "escaped"),
		"cat private",
		"cat " + filepath.Join(parent, "private"),
	} {
		if out, code := run(command); code == 0 {
			t.Errorf("worker escape succeeded: %s (%s)", command, out)
		}
	}
	if _, err := os.Stat(filepath.Join(parent, "parent-file")); err == nil {
		t.Fatal("parent checkout changed")
	}
	// A writable child alias to a parent inode must not bypass the path
	// denial. Read denials must likewise survive an alias into the child.
	if out, code := run("ln " + filepath.Join(parent, "tracked") + " linked-tracked"); code == 0 {
		if out, code := run("echo changed > linked-tracked"); code == 0 {
			t.Errorf("hardlink changed parent inode: %s", out)
		}
		b, err := os.ReadFile(filepath.Join(parent, "tracked"))
		if err != nil || string(b) != "parent" {
			t.Errorf("parent inode changed through hardlink: %q, %v", b, err)
		}
	} else {
		t.Logf("hardlink creation refused: %s", out)
	}
	if err := os.Link(filepath.Join(parent, "private"), filepath.Join(child, "private-alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ForCommand("cat private-alias", false).Wrap("/bin/sh", "cat private-alias", child); err == nil {
		t.Fatal("worker accepted a hardlink alias to denied file")
	}
	if err := os.Remove(filepath.Join(child, "private-alias")); err != nil {
		t.Fatal(err)
	}
	if out, code := run("ln private child-private-alias && cat child-private-alias"); code == 0 {
		t.Errorf("worker created a readable alias to denied child file: %s", out)
	}
	// Live listeners make these probes deterministic: an allowed connection
	// would succeed immediately, without depending on an external host.
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Log("socket probes skipped: python3 unavailable")
		return
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tcp.Close(); err != nil {
			t.Errorf("close TCP listener: %v", err)
		}
	}()
	sockDir, err := os.MkdirTemp("/tmp", "kiln-worker-sock-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.RemoveAll(sockDir); err != nil {
			t.Errorf("remove socket directory: %v", err)
		}
	}()
	unixPath := filepath.Join(sockDir, "outside.sock")
	unix, err := net.Listen("unix", unixPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unix.Close(); err != nil {
			t.Errorf("close Unix listener: %v", err)
		}
	}()
	for _, address := range []string{tcp.Addr().String(), unixPath} {
		command := python + ` -c 'import socket,sys; a=sys.argv[1]; s=socket.socket(socket.AF_UNIX if a.startswith("/") else socket.AF_INET); s.settimeout(1); s.connect(a if a.startswith("/") else ("127.0.0.1", int(a.rsplit(":",1)[1])))' ` + address
		if out, code := run(command); code == 0 {
			t.Errorf("worker connected to forbidden socket %s: %s", address, out)
		}
	}
	m.Close()
	if _, err := m.ForCommand("touch after-close", false).Wrap("/bin/sh", "touch after-close", child); err == nil {
		t.Fatal("closed worker manager still wraps commands")
	}
}

func TestWorkerRejectsFilesystemDisabled(t *testing.T) {
	if _, err := NewWorker(Config{FilesystemDisabled: true}, Options{}, "/", "/tmp"); err == nil {
		t.Fatal("filesystemDisabled accepted")
	}
}
