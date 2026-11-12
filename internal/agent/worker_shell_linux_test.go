//go:build linux

package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/msg"
)

func requireLinuxWorkerSandbox(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"bwrap", "socat", "setsid", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	if out, err := exec.Command("bwrap", "--ro-bind", "/", "/", "--dev", "/dev", "--unshare-net", "--unshare-pid", "--proc", "/proc", "--", "/bin/sh", "-c", "true").CombinedOutput(); err != nil {
		t.Skipf("bubblewrap cannot create a sandbox here: %v: %s", err, out)
	}
	base, err := os.MkdirTemp("/tmp", "kd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	t.Setenv("KILN_TMPDIR", filepath.Join(base, "t"))
}

func hostHasProcess(marker string) bool {
	files, _ := filepath.Glob("/proc/[0-9]*/cmdline")
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil && strings.Contains(string(b), marker) {
			return true
		}
	}
	return false
}

func waitProcess(marker string, want bool, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if hostHasProcess(marker) == want {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return hostHasProcess(marker) == want
}

func isolatedShellDispatcher(t *testing.T, command string) (*Dispatcher, string) {
	t.Helper()
	script := fmt.Sprintf("model: faux-1\nsteps:\n  - tool_call: {name: bash, args: {command: %q}, id: tc1}\n  - on_tool_result: tc1\n    then:\n      - text: done\n", command)
	d, _, _ := newParentAndDispatcher(t, script, nil)
	repo := testRepo(t)
	d.Env = execenv.New(repo)
	d.IsolatedWorkers = true
	d.IsolatedShell = true
	return d, repo
}

// An isolated worker's Bash writes its worktree, cannot reach the parent
// checkout, and a descendant that calls setsid() is gone by the time Dispatch
// returns, so removing the worktree cannot race a live process.
func TestDispatchIsolatedShellRunsAndOwnsDetachedDescendants(t *testing.T) {
	requireLinuxWorkerSandbox(t)
	// One look right after Dispatch returns, no polling, repeated: a race
	// would show up as an occasional survivor.
	for i := 0; i < 20; i++ {
		runIsolatedShellDispatch(t, fmt.Sprintf("4242.03%02d", i))
	}
}

func runIsolatedShellDispatch(t *testing.T, marker string) {
	t.Helper()
	parentFile := "/tmp/never-written-by-worker"
	d, repo := isolatedShellDispatcher(t, `printf from-worker > w.txt; printf x > `+parentFile+`; (setsid sleep `+marker+` </dev/null >/dev/null 2>&1 &); echo started`)
	var bashOutput string
	d.OnEvent = func(ev SubagentEvent) {
		if ev.Kind == SubagentEventTool && ev.ToolName == "bash" && ev.ToolResult != nil {
			bashOutput = msg.TextOf(ev.ToolResult.Content)
		}
	}
	result, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Prompt: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bashOutput, "started") {
		t.Fatalf("worker Bash did not run: %q", bashOutput)
	}
	if result.Artifact == nil || len(result.Artifact.ChangedFiles) != 1 || result.Artifact.ChangedFiles[0] != "w.txt" {
		t.Fatalf("artifact = %+v, want only w.txt", result.Artifact)
	}
	if _, err := os.Stat(parentFile); err == nil {
		os.Remove(parentFile)
		t.Fatal("worker wrote outside its worktree")
	}
	if _, err := os.Stat(filepath.Join(repo, "w.txt")); !os.IsNotExist(err) {
		t.Fatalf("worker wrote the parent checkout: %v", err)
	}
	if hostHasProcess(marker) {
		t.Fatalf("a setsid descendant (%s) outlived Dispatch", marker)
	}
}

// Cancelling a dispatch while its Bash runs stops detached descendants before
// Dispatch returns and removes the worktree.
func TestDispatchIsolatedShellCancelStopsDetachedDescendants(t *testing.T) {
	requireLinuxWorkerSandbox(t)
	const marker = "4242.0404"
	d, _ := isolatedShellDispatcher(t, `(setsid sleep `+marker+` </dev/null >/dev/null 2>&1 &); sleep 300`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := d.Dispatch(ctx, DispatchRequest{Agent: "general-purpose", Prompt: "run"})
		done <- err
	}()
	if !waitProcess(marker, true, 15*time.Second) {
		cancel()
		<-done
		t.Fatal("descendant never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("dispatch did not return after cancel")
	}
	if hostHasProcess(marker) {
		t.Fatal("a setsid descendant outlived a cancelled dispatch")
	}
}
