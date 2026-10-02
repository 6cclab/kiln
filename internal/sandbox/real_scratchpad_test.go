//go:build darwin || linux

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/execenv"
)

// The sandbox's writable temp dir is kiln's temp root, the parent of the
// session scratchpad: a sandboxed command's $TMPDIR is that root, and it
// can write the scratchpad the file tools use. A hard link from there to
// a file the sandbox does not let it write cannot be made.
func TestRealSandboxWritesSessionScratchpad(t *testing.T) {
	base := t.TempDir()
	t.Setenv("KILN_TMPDIR", base)
	t.Setenv("CLAUDE_CODE_TMPDIR", "")
	ws := filepath.Join(t.TempDir(), "ws")
	outside := filepath.Join(t.TempDir(), "outside")
	for _, d := range []string{ws, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(outside, "target")
	if err := os.WriteFile(target, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scratch, err := execenv.EnsureScratchpad(ws, "session-1")
	if err != nil {
		t.Fatal(err)
	}

	m := New(Config{Enabled: true}, Options{Cwd: ws, Home: filepath.Join(t.TempDir(), "home")})
	t.Cleanup(m.Close)
	if err := m.Unavailable(); err != nil {
		if runtime.GOOS == "linux" {
			t.Skipf("no Linux sandbox here: %v", err)
		}
		t.Fatalf("sandbox unavailable: %v", err)
	}
	env := execenv.New(ws)
	env.Sandbox = m
	run := func(cmd string) string {
		t.Helper()
		res, err := env.Exec(context.Background(), cmd, execenv.ExecOptions{
			InheritEnv: true, Timeout: 30 * time.Second, Sandbox: m.ForCommand(cmd, false)})
		if err != nil {
			t.Fatalf("exec %q: %v", cmd, err)
		}
		return res.Text
	}

	root, _ := execenv.RealPath(execenv.TempRoot())
	if out := run(`printf 'tmp=%s\n' "$TMPDIR"`); !strings.Contains(out, "tmp="+root) {
		t.Errorf("$TMPDIR inside the sandbox = %q, want kiln's temp root %s", out, root)
	}
	note := filepath.Join(scratch, "note.txt")
	if out := run("echo kept > " + shellQuote(note) + " && echo wrote"); !strings.Contains(out, "wrote") {
		t.Errorf("sandboxed write to the scratchpad failed: %s", out)
	}
	if b, _ := os.ReadFile(note); strings.TrimSpace(string(b)) != "kept" {
		t.Errorf("scratchpad file = %q", b)
	}

	link := filepath.Join(scratch, "link")
	run("ln " + shellQuote(target) + " " + shellQuote(link) + "; echo appended >> " + shellQuote(link))
	if b, _ := os.ReadFile(target); string(b) != "original\n" {
		t.Errorf("a hard link from the scratchpad let the sandbox write %s: %q", target, b)
	}
}
