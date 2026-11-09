package mods

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestProcessRun drives $.process.run: a subprocess gets stdin, its stdout and
// stderr come back, and a non-zero exit resolves (does not reject) with that
// exitCode. The command echoes stdin to stdout, writes "oops" to stderr and
// exits 3.
func TestProcessRun(t *testing.T) {
	h := newHarness(t, Options{Cwd: t.TempDir()})
	h.load(t, "proctier")
	h.e.SessionStart(ctxT(t))
	got := runCommand(t, h, "run", "")

	var out struct {
		ExitCode float64 `json:"exitCode"`
		Stdout   string  `json:"stdout"`
		Stderr   string  `json:"stderr"`
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("run = %q: %v", got, err)
	}
	if out.ExitCode != 3 {
		t.Errorf("exitCode = %v, want 3 (non-zero exit must resolve, not reject)", out.ExitCode)
	}
	if out.Stdout != "hello" {
		t.Errorf("stdout = %q, want %q (stdin round trip)", out.Stdout, "hello")
	}
	if out.Stderr != "oops" {
		t.Errorf("stderr = %q, want %q", out.Stderr, "oops")
	}
	if strings.Contains(h.logs.String(), "is not available in kiln") {
		t.Errorf("a $ call was rejected:\n%s", h.logs)
	}
}
