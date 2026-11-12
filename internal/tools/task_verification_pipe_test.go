package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/tool"
)

// A failing check whose exit status a pipeline (kiln's shell does not set
// pipefail), ||, a later statement, ! or a compound command can hide must not
// record a verification proof.
func TestVerificationBashToolPipedFailureRecordsNoProof(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell pipeline")
	}
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "source.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	readable := func(path string) bool { return path == "source.go" }
	wrapped := VerificationBashTool(BashTool(execenv.New(cwd)), readable)
	for _, tc := range []struct {
		command, masked string
	}{
		{`sh -c 'echo "--- FAIL: TestX"; exit 1' 2>&1 | tail -5`, "pipeline without pipefail"},
		{`sh -c 'exit 1' || true`, "|| fallback"},
		{`sh -c 'exit 1'; echo done`, "more than one statement"},
		{"sh -c 'exit 1'\necho done", "more than one statement"},
		{`! sh -c 'exit 1'`, "negated command"},
		{`{ sh -c 'exit 1'; }`, "compound command"},
	} {
		t.Run(tc.command, func(t *testing.T) {
			result := runVerification(t, wrapped, cwd, tc.command)
			if proof, ok := VerificationFromResult(result.Details); ok {
				t.Fatalf("failing check recorded a verification proof: %+v (isError=%t)", proof, result.IsError)
			}
			if attempt, ok := VerificationAttemptFromResult(result.Details); !ok || attempt.ExitStatusUnreliable != tc.masked {
				t.Errorf("attempt reason = %q (ok=%t), want %q", attempt.ExitStatusUnreliable, ok, tc.masked)
			}
		})
	}
	// Shapes whose exit status is the check's own still record evidence.
	for _, command := range []string{`sh -c 'exit 0'`, `cd . && sh -c 'exit 0' > /dev/null 2>&1`} {
		t.Run(command, func(t *testing.T) {
			if _, ok := VerificationFromResult(runVerification(t, wrapped, cwd, command).Details); !ok {
				t.Fatal("successful check recorded no proof")
			}
		})
	}
}

func runVerification(t *testing.T, wrapped *tool.Tool, cwd, command string) tool.Result {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"command": command, "verificationFiles": []string{"source.go"}})
	result, err := wrapped.Execute(context.Background(), raw, func(tool.Result) {}, tool.Invocation{Cwd: cwd, ToolCallID: "bash-1"})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
