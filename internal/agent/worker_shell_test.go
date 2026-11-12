package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/tool"
)

func TestWorkerShellActivationRejectsLinuxAndWindows(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			cleanup, err := enableWorkerShellForOS(execenv.New("/child"), execenv.New("/parent"), "/child", goos)
			if err == nil || !strings.Contains(err.Error(), "requires macOS Seatbelt") || cleanup != nil {
				t.Fatalf("activation cleanup present = %t, error = %v; want explicit platform error", cleanup != nil, err)
			}
		})
	}
}

func TestWorkerShellActivationRequiresConfinedFilesAndJobScope(t *testing.T) {
	if _, err := enableWorkerShellForOS(execenv.New("/child"), execenv.New("/parent"), "/child", "darwin"); err == nil {
		t.Fatal("unconfined worker shell accepted")
	}
}

func TestWorkerShellActivationCannotSpoofDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("requires a non-macOS host")
	}
	cleanup, err := enableWorkerShellForOS(execenv.New("/child"), execenv.New("/parent"), "/child", "darwin")
	if cleanup != nil || err == nil || !strings.Contains(err.Error(), "requires macOS Seatbelt") {
		t.Fatalf("spoofed platform activated worker shell: cleanup=%t error=%v", cleanup != nil, err)
	}
}

func TestGatedWorkerBashFailsClosedWithoutSandbox(t *testing.T) {
	root := t.TempDir()
	bash := gatedBashTool(execenv.New(root), nil, nil, nil, true)
	for _, tc := range []struct {
		name string
		raw  json.RawMessage
	}{
		{"malformed arguments", json.RawMessage("{")},
		{"missing sandbox", json.RawMessage(`{"command":"printf escaped > marker"}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := bash.Execute(context.Background(), tc.raw, func(tool.Result) {}, tool.Invocation{ToolName: "bash", Cwd: root})
			if err != nil || !result.IsError {
				t.Fatalf("unsafe Bash returned result=%+v error=%v", result, err)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(root, "marker")); !os.IsNotExist(err) {
		t.Fatalf("unsandboxed Bash ran: %v", err)
	}
}

func TestWorkerShellNoOptInLeavesEnvironmentUnchanged(t *testing.T) {
	const script = `
model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "printf escaped > marker"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "done"
`
	d, _, _ := newParentAndDispatcher(t, script, nil)
	repo := testRepo(t)
	d.Env = execenv.New(repo)
	d.IsolatedWorkers = true
	result, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Prompt: "run bash"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Artifact != nil {
		t.Fatalf("non-opt-in Bash changed the worker: %+v", result.Artifact)
	}
	if _, err := os.Stat(filepath.Join(repo, "marker")); !os.IsNotExist(err) {
		t.Fatalf("non-opt-in Bash ran in parent: %v", err)
	}
}

func TestDispatchIsolatedShellRequiresIsolatedWorkers(t *testing.T) {
	d, _, _ := newParentAndDispatcher(t, "model: faux-1\nsteps:\n  - text: unused\n", nil)
	d.IsolatedShell = true
	if _, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Prompt: "run"}); err == nil || !strings.Contains(err.Error(), "requires isolated workers") {
		t.Fatalf("isolated shell without worktree: %v", err)
	}
}

func TestDispatchIsolatedShellRefusesBeforeStartingWorker(t *testing.T) {
	d, _, _ := newParentAndDispatcher(t, "model: faux-1\nsteps:\n  - tool_call: {name: bash, args: {command: 'printf escaped > marker'}, id: tc1}\n", nil)
	repo := testRepo(t)
	d.Env = execenv.New(repo)
	d.IsolatedWorkers = true
	d.IsolatedShell = true
	result, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Prompt: "run bash"})
	if err == nil || !strings.Contains(err.Error(), "detached descendants cannot yet be owned safely") {
		t.Fatalf("unsafe isolated shell returned result=%+v error=%v", result, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "marker")); !os.IsNotExist(err) {
		t.Fatalf("worker Bash ran despite refusal: %v", err)
	}
}
