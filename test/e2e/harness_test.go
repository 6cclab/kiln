//go:build e2e

// Package e2e: shared test-binary harness. harness_test.go builds the real
// cmd/harness and cmd/faux binaries once (TestMain) and gives every other
// test in this package two helpers: startFaux (an in-process scripted model
// server, matching internal/cli/chat_test.go's own startFaux — the request
// log is only inspectable in-process, which is why this stays in-process
// rather than shelling out to the faux binary too) and runHarness (which
// execs the built harness binary against a scratch HOME/session store/
// project, the thing this whole package exists to drive).
package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/session/jsonl"
	tkfaux "github.com/andrepato/harness/internal/testkit/faux"
	"github.com/andrepato/harness/internal/testkit/fauxtest"
)

// harnessBin and fauxBin are absolute paths to binaries built once by
// TestMain, shared read-only by every test in this package.
var (
	harnessBin string
	fauxBin    string
)

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "harness-e2e-bin-")
	if err != nil {
		panic("harness e2e: mkdtemp: " + err.Error())
	}
	defer os.RemoveAll(tmp)

	harnessBin = filepath.Join(tmp, "kiln")
	if out, err := exec.Command("go", "build", "-o", harnessBin, "github.com/andrepato/harness/cmd/kiln").CombinedOutput(); err != nil {
		panic("kiln e2e: build cmd/kiln: " + err.Error() + "\n" + string(out))
	}

	fauxBin = filepath.Join(tmp, "faux")
	if out, err := exec.Command("go", "build", "-o", fauxBin, "github.com/andrepato/harness/cmd/faux").CombinedOutput(); err != nil {
		panic("harness e2e: build cmd/faux: " + err.Error() + "\n" + string(out))
	}

	os.Exit(m.Run())
}

// startFaux starts a scripted faux server in-process (so its recorded
// requests can be inspected directly, unlike a subprocess we'd have to
// scrape over HTTP) and returns its listen address plus the server itself
// (Requests() / Reset() / LoadScriptYAML()). Callers point the harness
// binary at addr via HARNESS_FAUX_ADDR/HARNESS_FAUX_API in runHarness's env.
func startFaux(t *testing.T, scriptYAML string) (addr string, srv *tkfaux.Server) {
	t.Helper()
	return fauxtest.Start(t, scriptYAML)
}

// scratchHome builds an isolated $HOME and session store root under
// t.TempDir(), so no test reads or writes the real user's ~/.harness.
func scratchHome(t *testing.T) (home, sessDir string) {
	t.Helper()
	home = t.TempDir()
	sessDir = t.TempDir()
	return home, sessDir
}

// scratchProject builds a fresh project directory containing src/math.js
// with a deliberate bug (`return a - b;` where add() should add), matching
// the fixture every print/session/hooks test in this package drives against.
func scratchProject(t *testing.T) string {
	t.Helper()
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	mathJS := "function add(a, b) {\n  return a - b;\n}\n\nfunction mul(a, b) {\n  return a * b;\n}\n"
	if err := os.WriteFile(filepath.Join(proj, "src", "math.js"), []byte(mathJS), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(proj)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// runResult is what runHarness hands back: everything a caller needs to
// assert on a completed run of the real binary.
type runResult struct {
	Stdout string
	Stderr string
	Code   int
}

// runHarness execs the built harness binary with dir as its working
// directory and env layered over a minimal, deterministic base (PATH plus
// whatever the caller passes — never the test runner's own HOME, HARNESS_*
// or OLLAMA_* so a test can never accidentally depend on this machine's
// state). args are the CLI arguments (no program name).
func runHarness(t *testing.T, dir string, env map[string]string, args ...string) runResult {
	t.Helper()
	return runBinary(t, harnessBin, dir, env, 60*time.Second, args...)
}

// runBinary is runHarness's shared machinery, parameterized on the binary
// and a timeout, so live_test.go (long model calls) can ask for more time.
func runBinary(t *testing.T, bin, dir string, env map[string]string, timeout time.Duration, args ...string) runResult {
	t.Helper()

	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", bin, err)
	}
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		code := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				t.Fatalf("run %s: %v", bin, err)
			}
		}
		return runResult{Stdout: stdout.String(), Stderr: stderr.String(), Code: code}
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("run %s: timed out after %s\nstdout so far:\n%s\nstderr so far:\n%s", bin, timeout, stdout.String(), stderr.String())
		return runResult{}
	}
}

// baseEnv builds the standard env map every test starts from: an isolated
// HOME and HARNESS_SESSIONS_DIR, plus the faux provider wired at addr. Model
// is pinned to the faux provider so no test depends on Ollama being
// reachable unless it explicitly opts in (live_test.go).
func baseEnv(home, sessDir, fauxAddr string) map[string]string {
	return map[string]string{
		"HOME":                 home,
		"HARNESS_SESSIONS_DIR": sessDir,
		"HARNESS_FAUX_ADDR":    fauxAddr,
		"HARNESS_FAUX_API":     "anthropic-messages",
		"HARNESS_MODEL":        "faux/faux-1",
	}
}

// sessionFile finds the single .jsonl session file under sessDir for proj,
// using jsonl's own directory-naming scheme. Fails the test if there isn't
// exactly one.
func sessionFile(t *testing.T, sessDir, proj string) string {
	t.Helper()
	files := sessionFiles(t, sessDir, proj)
	if len(files) != 1 {
		t.Fatalf("got %d session files under %s for %s, want exactly 1: %v", len(files), sessDir, proj, files)
	}
	return files[0]
}

// sessionFiles lists every .jsonl session file under sessDir for proj,
// oldest first would not be guaranteed — callers that care about order sort
// by mtime themselves (session_test.go's resume tests do).
func sessionFiles(t *testing.T, sessDir, proj string) []string {
	t.Helper()
	dir := filepath.Join(sessDir, jsonl.DirectoryName(proj))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read session dir %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".jsonl" {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}
