package mods

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestProcessRun drives $.process.run: a subprocess gets stdin, its stdout and
// stderr come back, and a non-zero exit resolves (does not reject) with that
// exitCode. The command echoes stdin to stdout, writes "oops" to stderr and
// exits 3. The permission gate is asked first, with the plugin, the argument
// vector, the cwd and the env overrides.
func TestProcessRun(t *testing.T) {
	dir := t.TempDir()
	g := &gateRecorder{}
	h := newHarness(t, Options{Cwd: dir, Services: Services{GateProcess: g.gate}})
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
	want := []gateCall{{Plugin: "proctier", Argv: []string{"sh", "-c", "cat; printf oops >&2; exit 3"},
		Cwd: dir, Env: map[string]string{"MOD_MARK": "kiln"}}}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !reflect.DeepEqual(g.calls, want) {
		t.Errorf("gate asked %+v, want %+v", g.calls, want)
	}
}

// A refusal from the permission gate rejects $.process.run with its reason,
// and nothing runs.
func TestProcessRunRefusedByGateRunsNothing(t *testing.T) {
	dir := t.TempDir()
	g := &gateRecorder{deny: "denied by rule Bash(sh:*)"}
	h := newHarness(t, Options{Cwd: dir, Services: Services{GateProcess: g.gate}})
	h.load(t, "proctier")
	h.e.SessionStart(ctxT(t))
	got := runCommand(t, h, "mark", "")
	if !strings.Contains(got, "refused") || !strings.Contains(got, "denied by rule Bash(sh:*)") {
		t.Errorf("mark = %q, want a refusal carrying the gate's reason", got)
	}
	if g.count() != 1 {
		t.Errorf("gate asked %d times, want 1", g.count())
	}
	if _, err := os.Stat(filepath.Join(dir, "ran")); err == nil {
		t.Error("the program ran although the gate refused it")
	}
}

// With no permission gate wired, $.process.run rejects and nothing runs: a
// mod's process never runs ungated.
func TestProcessRunWithoutGateRunsNothing(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, Options{Cwd: dir})
	h.load(t, "proctier")
	h.e.SessionStart(ctxT(t))
	got := runCommand(t, h, "mark", "")
	if !strings.Contains(got, "permission gate") {
		t.Errorf("mark = %q, want a refusal naming the missing permission gate", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "ran")); err == nil {
		t.Error("the program ran with no gate")
	}
}
