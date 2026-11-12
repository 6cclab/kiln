package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/tool"
)

func TestWorkerShellActivationRejectsWindows(t *testing.T) {
	cleanup, err := enableWorkerShellForOS(execenv.New("/child"), execenv.New("/parent"), "/child", "windows")
	if err == nil || !strings.Contains(err.Error(), "requires macOS Seatbelt or Linux bubblewrap") || cleanup != nil {
		t.Fatalf("activation cleanup present = %t, error = %v; want explicit platform error", cleanup != nil, err)
	}
}

func TestIsolatedShellSupport(t *testing.T) {
	if err := isolatedShellSupport("linux"); err != nil {
		t.Errorf("linux refused: %v", err)
	}
	for goos, want := range map[string]string{"darwin": "setsid()", "windows": "no worktree-aware command sandbox", "freebsd": "no worktree-aware command sandbox"} {
		if err := isolatedShellSupport(goos); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want refusal mentioning %q", goos, err, want)
		}
	}
}

func TestWorkerShellActivationRequiresConfinedFilesAndJobScope(t *testing.T) {
	if _, err := enableWorkerShellForOS(execenv.New("/child"), execenv.New("/parent"), "/child", "darwin"); err == nil {
		t.Fatal("unconfined worker shell accepted")
	}
}

func TestWorkerShellActivationCannotSpoofPlatform(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		if goos == runtime.GOOS {
			continue
		}
		cleanup, err := enableWorkerShellForOS(execenv.New("/child"), execenv.New("/parent"), "/child", goos)
		if cleanup != nil || err == nil || !strings.Contains(err.Error(), "requires macOS Seatbelt or Linux bubblewrap") {
			t.Errorf("spoofed %s activated worker shell: cleanup=%t error=%v", goos, cleanup != nil, err)
		}
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
	if isolatedShellSupport(runtime.GOOS) == nil {
		t.Skip("isolated shell is supported here; see worker_shell_linux_test.go")
	}
	d, _, _ := newParentAndDispatcher(t, "model: faux-1\nsteps:\n  - tool_call: {name: bash, args: {command: 'printf escaped > marker'}, id: tc1}\n", nil)
	repo := testRepo(t)
	d.Env = execenv.New(repo)
	d.IsolatedWorkers = true
	d.IsolatedShell = true
	result, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Prompt: "run bash"})
	if err == nil || !strings.Contains(err.Error(), "isolated shell is unavailable on") {
		t.Fatalf("unsafe isolated shell returned result=%+v error=%v", result, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "marker")); !os.IsNotExist(err) {
		t.Fatalf("worker Bash ran despite refusal: %v", err)
	}
}

// A worktree whose shell processes are not confirmed stopped stays on disk and
// the failure is returned; a confirmed one is removed.
func TestReleaseWorkerWorktreeKeepsWorktreeWhenShellNotStopped(t *testing.T) {
	repo := testRepo(t)
	w, _, err := newWorkerWorktree(context.Background(), execenv.New(repo))
	if err != nil {
		t.Fatal(err)
	}
	stuck := errors.New("namespace still running after 5s")
	err = releaseWorkerWorktree(stuck, w)
	if err == nil || !errors.Is(err, stuck) || !strings.Contains(err.Error(), "kept") {
		t.Fatalf("err = %v, want a kept-worktree error wrapping the cause", err)
	}
	if _, statErr := os.Stat(w.path); statErr != nil {
		t.Fatalf("worktree removed despite unconfirmed shell: %v", statErr)
	}
	if err := releaseWorkerWorktree(nil, w); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(w.path); !os.IsNotExist(statErr) {
		t.Fatalf("worktree not removed after confirmed stop: %v", statErr)
	}
}

// Activation fails closed, leaving no sandbox and no cleanup, when the parent's
// sandbox is not kiln's own or the worker sandbox cannot be built (here the
// worker directory is not a linked Git worktree).
func TestWorkerShellActivationFailsClosed(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("worker shell activation is refused earlier on " + runtime.GOOS)
	}
	for _, tc := range []struct {
		name, want string
		sandbox    execenv.Sandbox
	}{
		{"unknown parent sandbox", "unknown parent sandbox", foreignSandbox{}},
		{"worker sandbox unavailable", "prepare isolated worker shell sandbox", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parentRoot, child := t.TempDir(), t.TempDir()
			workerEnv, err := execenv.New(child).CloneForConfinedFiles(child)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = workerEnv.CloseConfinedFiles() })
			workerEnv.JobScope = execenv.NewJobScope()
			t.Cleanup(workerEnv.JobScope.Close)
			parentEnv := execenv.New(parentRoot)
			parentEnv.Sandbox = tc.sandbox
			cleanup, err := enableWorkerShellForOS(workerEnv, parentEnv, child, runtime.GOOS)
			if err == nil || !strings.Contains(err.Error(), tc.want) || cleanup != nil {
				t.Fatalf("cleanup present = %t, error = %v; want a refusal mentioning %q", cleanup != nil, err, tc.want)
			}
			if workerEnv.Sandbox != nil {
				t.Fatal("refused activation left a sandbox on the worker")
			}
		})
	}
}

type foreignSandbox struct{}

func (foreignSandbox) Active() bool                                   { return true }
func (foreignSandbox) ForCommand(string, bool) execenv.CommandSandbox { return nil }
func (foreignSandbox) OffersUnsandboxed() bool                        { return false }
