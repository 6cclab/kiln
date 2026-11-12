package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/execenv"
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
