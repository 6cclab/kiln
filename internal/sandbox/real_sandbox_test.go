//go:build darwin || linux

package sandbox

// These tests run commands under the real mechanism — /usr/bin/sandbox-exec
// with the profile kiln generates on macOS, bubblewrap on Linux — through
// execenv.Exec exactly as the bash tool does. On macOS they always run; on
// Linux they skip when bwrap or socat is missing. They need no network: the "internet" is an httptest server on
// loopback, reached through kiln's proxy by an allowlisted name the test
// resolver maps to it. Every path is under t.TempDir(), including HOME.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/execenv"
)

type realRig struct {
	t       *testing.T
	scratch string // everything lives under here
	home    string
	ws      string
	outside string
	m       *Manager
	env     *execenv.Env
}

func newRealRig(t *testing.T, cfg Config, lookup map[string]string) *realRig {
	t.Helper()
	scratch := t.TempDir()
	r := &realRig{t: t, scratch: scratch,
		home:    filepath.Join(scratch, "home"),
		ws:      filepath.Join(scratch, "ws"),
		outside: filepath.Join(scratch, "outside"),
	}
	for _, d := range []string{r.home, r.ws, r.outside, filepath.Join(r.home, ".ssh")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tmp := filepath.Join(scratch, "tmp")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = true
	opts := Options{Cwd: r.ws, Home: r.home, TmpDir: tmp}
	if lookup != nil {
		opts.LookupIP = func(_ context.Context, host string) ([]netip.Addr, error) {
			if ip, ok := lookup[host]; ok {
				return []netip.Addr{netip.MustParseAddr(ip)}, nil
			}
			return nil, fmt.Errorf("no such host %s", host)
		}
	}
	r.m = New(cfg, opts)
	t.Cleanup(r.m.Close)
	if err := r.m.Unavailable(); err != nil {
		if runtime.GOOS == "linux" {
			t.Skipf("no Linux sandbox here: %v", err)
		}
		t.Fatalf("sandbox unavailable on this Mac: %v", err)
	}
	r.env = execenv.New(r.ws)
	r.env.Sandbox = r.m
	return r
}

// run runs command sandboxed (as the bash tool would) and returns its
// combined output and exit code.
func (r *realRig) run(command string) (string, int) {
	r.t.Helper()
	sb := r.m.ForCommand(command, false)
	if sb == nil {
		r.t.Fatalf("command %q would not be sandboxed", command)
	}
	res, err := r.env.Exec(context.Background(), command, execenv.ExecOptions{
		InheritEnv: true,
		Timeout:    60 * time.Second,
		Sandbox:    sb,
	})
	if err != nil {
		r.t.Fatalf("exec %q: %v", command, err)
	}
	// A profile sandbox-exec cannot load fails every command, which a test
	// expecting a refusal would take for one.
	if strings.Contains(res.Text, "sandbox-exec: ") || strings.Contains(res.Text, "bwrap: ") {
		r.t.Fatalf("the sandbox itself failed for %q: %s", command, res.Text)
	}
	return res.Text, res.ExitCode
}

func mustNotExist(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); err == nil {
		t.Errorf("%s exists: the sandbox let the write through", p)
	}
}

func TestRealSandboxWrites(t *testing.T) {
	r := newRealRig(t, Config{AllowUnsandboxed: true}, nil)

	out, code := r.run("touch inside.txt && echo made")
	if code != 0 || !strings.Contains(out, "made") {
		t.Fatalf("write inside the workspace failed: %d %q", code, out)
	}
	if _, err := os.Stat(filepath.Join(r.ws, "inside.txt")); err != nil {
		t.Fatalf("inside.txt missing: %v", err)
	}

	out, code = r.run("touch " + filepath.Join(r.outside, "x"))
	if code == 0 || !strings.Contains(out, refusedWrite()) {
		t.Errorf("write outside the workspace: exit %d %q, want %s", code, out, refusedWrite())
	}
	mustNotExist(t, filepath.Join(r.outside, "x"))

	// $TMPDIR points at the sandbox's own temp directory, which is writable.
	out, code = r.run(`d=$(mktemp -d "$TMPDIR/x.XXXXXX") && touch "$d/f" && echo "tmp=$TMPDIR"`)
	if code != 0 || !strings.Contains(out, "tmp="+realPath(filepath.Join(r.scratch, "tmp"))) {
		t.Errorf("temp dir: %d %q", code, out)
	}
}

// A symlink in the workspace pointing outside it does not make its target
// writable: Seatbelt judges the resolved path.
func TestRealSandboxSymlinkEscape(t *testing.T) {
	r := newRealRig(t, Config{}, nil)
	if err := os.Symlink(r.outside, filepath.Join(r.ws, "link")); err != nil {
		t.Fatal(err)
	}
	out, code := r.run("echo pwned > link/f")
	if code == 0 {
		t.Errorf("write through a symlink escaped: %q", out)
	}
	mustNotExist(t, filepath.Join(r.outside, "f"))

	// A link created inside the sandbox fares no better.
	out, code = r.run("ln -s " + r.home + " homelink && echo x > homelink/.bashrc")
	if code == 0 {
		t.Errorf("write through a new symlink escaped: %q", out)
	}
	mustNotExist(t, filepath.Join(r.home, ".bashrc"))
}

// Claude Code's defaults: the home directory is not writable (so neither
// ~/.bashrc nor ~/.ssh), while reads are allowed everywhere, credential
// files included, unless denyRead says otherwise; inside the workspace the
// protected paths (.git/hooks, .git/config, .claude settings, shell
// startup files) stay unwritable.
func TestRealSandboxDefaultsHomeAndGit(t *testing.T) {
	r := newRealRig(t, Config{}, nil)
	if err := os.WriteFile(filepath.Join(r.home, ".ssh", "id_test"), []byte("KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = r.ws
		cmd.Env = append(os.Environ(), "HOME="+r.home, "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@example.com")
	git("config", "user.name", "t")

	cases := []struct {
		name, cmd, path string
	}{
		{"~/.bashrc", "echo evil >> " + filepath.Join(r.home, ".bashrc"), filepath.Join(r.home, ".bashrc")},
		{"~/.ssh", "echo evil > " + filepath.Join(r.home, ".ssh", "authorized_keys"), filepath.Join(r.home, ".ssh", "authorized_keys")},
		{".git/hooks", "echo evil > .git/hooks/pre-commit", filepath.Join(r.ws, ".git", "hooks", "pre-commit")},
		{".git/hooks, other case (macOS)", "echo evil > .GIT/HOOKS/post-checkout", filepath.Join(r.ws, ".git", "hooks", "post-checkout")},
		{".claude/settings.json", "mkdir -p .claude; echo '{}' > .claude/settings.json", filepath.Join(r.ws, ".claude", "settings.json")},
		{".claude/settings.local.json, other case (macOS)", "echo '{}' > .CLAUDE/SETTINGS.LOCAL.JSON", filepath.Join(r.ws, ".claude", "settings.local.json")},
		{".kiln", "mkdir -p .kiln && echo '{}' > .kiln/settings.local.json", filepath.Join(r.ws, ".kiln", "settings.local.json")},
		{"workspace .bashrc", "echo evil > .bashrc", filepath.Join(r.ws, ".bashrc")},
		{".mcp.json", "echo '{}' > .mcp.json", filepath.Join(r.ws, ".mcp.json")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if strings.HasSuffix(c.name, "(macOS)") && runtime.GOOS != "darwin" {
				t.Skip("case-insensitive filesystem only")
			}
			out, code := r.run(c.cmd)
			if code == 0 {
				t.Errorf("%s: write allowed: %q", c.cmd, out)
			}
			mustNotExist(t, c.path)
		})
	}

	// .git/config may not change; the swap trick (rename .git away and put
	// a doctored one in its place) is refused too.
	before, _ := os.ReadFile(filepath.Join(r.ws, ".git", "config"))
	if out, code := r.run("echo '[core]' >> .git/config"); code == 0 {
		t.Errorf(".git/config append allowed: %q", out)
	}
	if out, code := r.run("mv .git .git-old"); code == 0 {
		t.Errorf("renaming .git allowed: %q", out)
	}
	after, _ := os.ReadFile(filepath.Join(r.ws, ".git", "config"))
	if string(before) != string(after) {
		t.Error(".git/config changed")
	}

	// Ordinary git work still runs: objects, refs and the index are not
	// protected.
	out, code := r.run("echo hi > a.txt && git add a.txt && git commit -qm first && git log --oneline | wc -l")
	if code != 0 || strings.TrimSpace(out) != "1" {
		t.Errorf("git commit in the sandbox: %d %q", code, out)
	}

	// Reads stay open, credentials included (Claude Code's default).
	out, code = r.run("cat " + filepath.Join(r.home, ".ssh", "id_test"))
	if code != 0 || !strings.Contains(out, "KEY") {
		t.Errorf("reading ~/.ssh: %d %q (reads are allowed by default)", code, out)
	}
}

// denyRead blocks a path; allowRead re-opens a narrower one inside it; a
// narrower denyRead holds inside a broader allowRead.
func TestRealSandboxReadRules(t *testing.T) {
	scratchHome := func(r *realRig, rel, body string) {
		p := filepath.Join(r.home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Built after the rig exists, since the rules name its home.
	r := newRealRig(t, Config{}, nil)
	r.m.cfg.DenyRead = []Rule{{Path: r.home}, {Path: r.home, Segs: []string{"**", ".env"}}}
	r.m.cfg.AllowRead = []Rule{{Path: filepath.Join(r.home, "projects")}}
	scratchHome(r, "secret.txt", "SECRET")
	scratchHome(r, "projects/readme.txt", "README")
	scratchHome(r, "projects/app/.env", "TOKEN")

	if out, code := r.run("cat " + filepath.Join(r.home, "secret.txt")); code == 0 || strings.Contains(out, "SECRET") {
		t.Errorf("denyRead ~ did not block: %d %q", code, out)
	}
	if out, code := r.run("cat " + filepath.Join(r.home, "projects", "readme.txt")); code != 0 || !strings.Contains(out, "README") {
		t.Errorf("allowRead ~/projects did not re-open: %d %q", code, out)
	}
	if runtime.GOOS != "darwin" {
		return // wildcard read rules are macOS-only for now (bwrap.go)
	}
	if out, code := r.run("cat " + filepath.Join(r.home, "projects", "app", ".env")); code == 0 || strings.Contains(out, "TOKEN") {
		t.Errorf("wildcard denyRead inside allowRead did not hold: %d %q", code, out)
	}
}

// The network: nothing but kiln's proxy is reachable, and the proxy lets
// through only allowlisted hosts. The target is a local server, reached
// by an allowlisted name (and its address allowlisted, as the docs
// require for a name that resolves to loopback).
func TestRealSandboxNetwork(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "hello-from-allowed")
	}))
	defer srv.Close()
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "hello-over-tls")
	}))
	defer tlsSrv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	tlsPort := tlsSrv.Listener.Addr().(*net.TCPAddr).Port

	cfg := Config{
		AllowUnsandboxed: true,
		AllowedDomains:   []string{"allowed.test", fmt.Sprintf("127.0.0.1:%d", port), fmt.Sprintf("127.0.0.1:%d", tlsPort)},
		StrictAllowlist:  true,
	}
	r := newRealRig(t, cfg, map[string]string{"allowed.test": "127.0.0.1", "blocked.test": "127.0.0.1"})

	out, code := r.run(fmt.Sprintf("curl -sS -m 10 http://allowed.test:%d/", port))
	if code != 0 || !strings.Contains(out, "hello-from-allowed") {
		t.Fatalf("allowlisted host through the proxy: %d %q", code, out)
	}
	out, code = r.run(fmt.Sprintf("curl -sSk -m 10 https://allowed.test:%d/", tlsPort))
	if code != 0 || !strings.Contains(out, "hello-over-tls") {
		t.Fatalf("allowlisted host over CONNECT: %d %q", code, out)
	}

	cmd := fmt.Sprintf("curl -sSf -m 10 http://blocked.test:%d/", port)
	sb := r.m.ForCommand(cmd, false)
	res, err := r.env.Exec(context.Background(), cmd, execenv.ExecOptions{InheritEnv: true, Timeout: time.Minute, Sandbox: sb})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == 0 || strings.Contains(res.Text, "hello") {
		t.Fatalf("non-allowlisted host went through: %d %q", res.ExitCode, res.Text)
	}
	note := sb.Explain(res.Text, res.ExitCode)
	if !strings.Contains(note, "blocked.test") || !strings.Contains(note, "dangerouslyDisableSandbox") {
		t.Errorf("failure note does not name the blocked host: %q", note)
	}

	// Going around the proxy fails: no DNS, no direct connection.
	out, code = r.run(fmt.Sprintf("curl -sS -m 10 --noproxy '*' http://127.0.0.1:%d/", port))
	if code == 0 || strings.Contains(out, "hello") {
		t.Errorf("direct connection bypassed the proxy: %d %q", code, out)
	}
	out, code = r.run("curl -sS -m 10 --noproxy '*' http://example.com/")
	if code == 0 || !strings.Contains(out, "Could not resolve host") {
		t.Errorf("DNS outside the proxy: %d %q", code, out)
	}
}

// Credential env vars listed with mode deny are removed.
func TestRealSandboxDenyEnv(t *testing.T) {
	t.Setenv("KILN_TEST_SECRET", "s3cret")
	r := newRealRig(t, Config{DenyEnv: []string{"KILN_TEST_SECRET"}}, nil)
	out, code := r.run(`echo "[${KILN_TEST_SECRET:-unset}] [$KILN_SANDBOX]"`)
	if code != 0 || !strings.Contains(out, "[unset] [1]") {
		t.Errorf("env: %d %q", code, out)
	}
}

// refusedWrite is what a write the sandbox refuses prints.
func refusedWrite() string {
	if runtime.GOOS == "linux" {
		return "Read-only file system"
	}
	return "Operation not permitted"
}

// The user's terminal is out of reach: a sandboxed command cannot open a
// tty device this user owns (where it could read keystrokes or inject
// input), though the same open works outside the sandbox.
func TestRealSandboxTerminal(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Linux: bwrap's --new-session and private /dev cover this")
	}
	var tty string
	matches, _ := filepath.Glob("/dev/ttys*")
	for _, m := range matches {
		if f, err := os.OpenFile(m, os.O_RDONLY, 0); err == nil {
			f.Close()
			tty = m
			break
		}
	}
	if tty == "" {
		t.Skip("no terminal device this user can open")
	}
	r := newRealRig(t, Config{}, nil)
	if out, code := r.run("exec 3<" + tty + " && echo opened"); code == 0 || strings.Contains(out, "opened") {
		t.Errorf("sandboxed command opened %s for reading: %q", tty, out)
	}
	if out, code := r.run("exec 3>" + tty + " && echo opened"); code == 0 || strings.Contains(out, "opened") {
		t.Errorf("sandboxed command opened %s for writing: %q", tty, out)
	}
	if out, code := r.run("echo ok > /dev/null && echo fine"); code != 0 || !strings.Contains(out, "fine") {
		t.Errorf("/dev/null must stay writable: %d %q", code, out)
	}
}

// A submodule's git directory is as protected as the top-level one: its
// config (whose core.fsmonitor git status runs, outside the sandbox) and
// hooks cannot be written, including in a module directory created after
// the command started (macOS; Linux binds the ones that exist).
func TestRealSandboxSubmoduleGitDir(t *testing.T) {
	r := newRealRig(t, Config{}, nil)
	mod := filepath.Join(r.ws, ".git", "modules", "sub")
	if err := os.MkdirAll(filepath.Join(mod, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(mod, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	os.WriteFile(filepath.Join(mod, "config"), []byte("[core]\n"), 0o644)

	for _, cmd := range []string{
		`echo "fsmonitor = /tmp/evil" >> .git/modules/sub/config`,
		"echo evil > .git/modules/sub/hooks/post-checkout",
		"echo evil > .git/config.worktree",
	} {
		if out, code := r.run(cmd); code == 0 {
			t.Errorf("%s: allowed: %q", cmd, out)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(mod, "config")); strings.Contains(string(b), "fsmonitor") {
		t.Error("submodule config changed")
	}
	mustNotExist(t, filepath.Join(mod, "hooks", "post-checkout"))
	if runtime.GOOS == "darwin" {
		if out, code := r.run("mkdir -p .git/modules/new/hooks && echo evil > .git/modules/new/config"); code == 0 {
			t.Errorf("new module config allowed: %q", out)
		}
		mustNotExist(t, filepath.Join(r.ws, ".git", "modules", "new", "config"))
	}
	// The rest of .git stays writable: git needs its index and objects.
	if out, code := r.run("echo x > .git/modules/sub/index && echo ok"); code != 0 {
		t.Errorf("module index write refused: %q", out)
	}
}
