//go:build darwin

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/andrepato/harness/internal/claude/permission"
	"github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
	"github.com/andrepato/harness/internal/tools"
)

func TestWorkerShellLiveSeatbeltThroughBashTool(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("Seatbelt unavailable")
	}
	base := t.TempDir()
	t.Setenv("KILN_TMPDIR", filepath.Join(base, "temp"))
	parent := filepath.Join(base, "parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	testGit(t, parent, "init", "-q")
	testGit(t, parent, "config", "user.name", "Kiln Test")
	testGit(t, parent, "config", "user.email", "kiln@example.test")
	if err := os.WriteFile(filepath.Join(parent, "tracked"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, parent, "add", "tracked")
	testGit(t, parent, "commit", "-qm", "base")
	child := filepath.Join(base, "child")
	testGit(t, parent, "worktree", "add", "-qb", "worker", child)
	t.Cleanup(func() { testGit(t, parent, "worktree", "remove", "--force", child) })
	parentEnv := execenv.New(parent)
	workerEnv, err := parentEnv.CloneForConfinedFiles(child)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workerEnv.CloseConfinedFiles() })
	workerEnv.JobScope = execenv.NewJobScope()
	cleanup, err := enableWorkerShellForOS(workerEnv, parentEnv, child, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { workerEnv.JobScope.Close(); cleanup() })
	if workerEnv.Sandbox == nil || !workerEnv.Sandbox.Active() || workerEnv.Sandbox.OffersUnsandboxed() {
		t.Fatal("worker sandbox unavailable or permits unsandboxed execution")
	}
	bash := tools.BashTool(workerEnv)
	if strings.Contains(string(bash.Parameters), "dangerouslyDisableSandbox") {
		t.Fatal("worker bash exposes unsandboxed escape")
	}
	run := func(command string) tool.Result {
		t.Helper()
		args, _ := json.Marshal(map[string]any{"command": command})
		result, err := bash.Execute(context.Background(), args, func(tool.Result) {}, tool.Invocation{ToolName: "bash", Cwd: child})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(msg.TextOf(result.Content), "sandbox-exec:") {
			t.Fatalf("Seatbelt profile failed: %s", msg.TextOf(result.Content))
		}
		return result
	}
	if result := run("printf child > child-file && printf temp > \"$TMPDIR/worker-file\""); result.IsError {
		t.Fatalf("child write failed: %s", msg.TextOf(result.Content))
	}
	if got, err := os.ReadFile(filepath.Join(child, "child-file")); err != nil || string(got) != "child" {
		t.Fatalf("child write = %q, %v", got, err)
	}
	for _, command := range []string{
		"printf escape > " + filepath.Join(parent, "escape"),
		"printf escape > " + filepath.Join(parent, ".git", "config"),
		"printf escape > " + filepath.Join(child, ".git"),
		"printf escape > " + filepath.Join(base, "temp", "escape"),
	} {
		if result := run(command); !result.IsError {
			t.Errorf("write escaped: %s", command)
		}
	}
	if _, err := os.Stat(filepath.Join(parent, "escape")); !os.IsNotExist(err) {
		t.Fatalf("parent write escaped: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(parent, ".git", "config")); err != nil || strings.Contains(string(got), "escape") {
		t.Fatalf("Git metadata changed: %q, %v", got, err)
	}
	sockDir, err := os.MkdirTemp("/tmp", "kiln-shell-sock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sock, err := net.Listen("unix", filepath.Join(sockDir, "listener.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sock.Close() })
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable for Unix socket probe")
	}
	probe := python + ` -c 'import socket; s=socket.socket(socket.AF_UNIX); s.settimeout(1); s.connect("` + sock.Addr().String() + `")'`
	if result := run(probe); !result.IsError {
		t.Fatal("worker connected to Unix socket")
	}
	workerEnv.JobScope.Close()
	if result := run("printf late > after-close"); !result.IsError {
		t.Fatal("worker command started after process scope closed")
	}
}

func TestDispatchIsolatedShellRefusesDetachedDescendants(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("Seatbelt unavailable")
	}
	repo := testRepo(t)
	command := fmt.Sprintf("python3 -c %q & echo started", "import os,time; os.setsid(); print('DETACHEDPID='+str(os.getpid()),flush=True); time.sleep(10)")
	script := fmt.Sprintf("model: faux-1\nsteps:\n  - tool_call: {name: bash, args: {command: %q}, id: tc1}\n", command)
	for _, tc := range []struct {
		name   string
		cancel bool
	}{{"normal", false}, {"canceled", true}} {
		t.Run(tc.name, func(t *testing.T) {
			var bashOutput string
			t.Cleanup(func() {
				match := regexp.MustCompile(`DETACHEDPID=(\d+)`).FindStringSubmatch(bashOutput)
				if len(match) == 2 {
					if pid, err := strconv.Atoi(match[1]); err == nil {
						_ = syscall.Kill(pid, syscall.SIGKILL)
					}
				}
			})
			d, _, _ := newParentAndDispatcher(t, script, nil)
			d.Env = execenv.New(repo)
			d.IsolatedWorkers = true
			d.IsolatedShell = true
			d.OnEvent = func(ev SubagentEvent) {
				if ev.Kind == SubagentEventTool && ev.ToolName == "bash" && ev.ToolResult != nil {
					bashOutput = msg.TextOf(ev.ToolResult.Content)
				}
			}
			ctx := context.Background()
			if tc.cancel {
				var stop context.CancelFunc
				ctx, stop = context.WithCancel(ctx)
				stop()
			}
			if _, err := d.Dispatch(ctx, DispatchRequest{Agent: "general-purpose", Prompt: "detach"}); err == nil || !strings.Contains(err.Error(), "detached descendants cannot yet be owned safely") {
				t.Fatalf("unsafe isolated shell was not refused: %v", err)
			}
			if bashOutput != "" {
				t.Fatalf("child Bash ran before refusal: %q", bashOutput)
			}
		})
	}
}

type deniedBashRewriteHooks struct{ recordingHooks }

func (h *deniedBashRewriteHooks) Attach(_ string, _ string, child *harness.Harness) {
	child.Hooks().OnBeforeTool(func(_ context.Context, call msg.ToolCall) (harness.BeforeToolResult, error) {
		if call.Name != "bash" {
			return harness.BeforeToolResult{}, nil
		}
		raw, _ := json.Marshal(map[string]any{"command": "printf forbidden > child-file"})
		return harness.BeforeToolResult{RewrittenArgs: raw}, nil
	})
}

func TestDispatchChildBashGatesFinalRewrittenArgs(t *testing.T) {
	repo := testRepo(t)
	gate := permission.NewGate(permission.GateOptions{
		Permissions: settings.Permissions{Deny: []string{"Bash(printf forbidden*)"}},
		Mode:        settings.ModeManual, Roots: []string{repo},
	})
	blocked, err := gate.Check(context.Background(), permission.Request{ToolName: "bash", PrimaryArg: "printf forbidden > child-file", Args: map[string]any{"command": "printf forbidden > child-file"}})
	if err != nil || blocked == nil {
		t.Fatalf("deny-rule control failed: block=%v err=%v", blocked, err)
	}
	const script = "model: faux-1\nsteps:\n  - tool_call: {name: bash, args: {command: 'echo safe'}, id: tc1}\n  - on_tool_result: tc1\n    then:\n      - text: done\n"
	d, _, _ := newParentAndDispatcher(t, script, gate)
	d.Env = execenv.New(repo)
	d.Hooks = &deniedBashRewriteHooks{}
	var bashResult *msg.ToolResultMessage
	d.OnEvent = func(ev SubagentEvent) {
		if ev.Kind == SubagentEventTool && ev.ToolName == "bash" {
			bashResult = ev.ToolResult
		}
	}
	if _, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Prompt: "run"}); err != nil {
		t.Fatal(err)
	}
	if bashResult == nil || !bashResult.IsError {
		t.Fatalf("rewritten denied Bash did not fail: %+v", bashResult)
	}
	if _, err := os.Stat(filepath.Join(repo, "child-file")); !os.IsNotExist(err) {
		t.Fatalf("denied rewritten Bash wrote file: %v", err)
	}
}
