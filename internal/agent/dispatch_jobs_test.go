//go:build !windows

package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/execenv"
)

// A background child can outlive the bash tool's shell. Its lifetime must
// still end with the worker, while a process started by the parent survives.
func TestDispatchStopsWorkerBackgroundJobsWithoutStoppingParent(t *testing.T) {
	d, parentSession, cwd := newParentAndDispatcher(t, workerBackgroundScript("worker.pid"), nil)
	t.Cleanup(func() { _ = parentSession.Harness.Close() })
	parent, err := d.Env.Exec(context.Background(), "sleep 30 & echo $! > parent.pid", execenv.ExecOptions{})
	if err != nil || !parent.JobsLeft {
		t.Fatalf("start parent job: %+v, %v", parent, err)
	}
	parentPID := readJobPID(t, filepath.Join(cwd, "parent.pid"))
	t.Cleanup(func() { _ = syscall.Kill(parentPID, syscall.SIGTERM) })

	result, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Prompt: "start background job"})
	if err != nil || result.ToolCalls != 1 {
		t.Fatalf("Dispatch: %+v, %v", result, err)
	}
	workerPID := readJobPID(t, filepath.Join(cwd, "worker.pid"))
	t.Cleanup(func() { _ = syscall.Kill(workerPID, syscall.SIGTERM) })
	if waitJobGone(workerPID, 2*time.Second) == false {
		t.Fatalf("worker background process %d survived Dispatch", workerPID)
	}
	if !jobRunning(parentPID) {
		t.Fatalf("parent background process %d was stopped with worker", parentPID)
	}
	if d.Env.JobScope != nil {
		t.Fatal("Dispatch mutated the parent's execution environment")
	}
}

func TestDispatchCancellationStopsWorkerBackgroundJobs(t *testing.T) {
	script := `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "sleep 30 & echo $! > worker.pid"}, id: tc1}
  - on_tool_result: tc1
    then:
      - delay: 10s
      - text: "too late"
`
	d, parentSession, cwd := newParentAndDispatcher(t, script, nil)
	t.Cleanup(func() { _ = parentSession.Harness.Close() })
	toolEnded := make(chan struct{}, 1)
	d.OnEvent = func(ev SubagentEvent) {
		if ev.Kind == SubagentEventTool && ev.ToolName == "bash" {
			select {
			case toolEnded <- struct{}{}:
			default:
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type dispatchOutcome struct {
		result DispatchResult
		err    error
	}
	done := make(chan dispatchOutcome, 1)
	go func() {
		result, err := d.Dispatch(ctx, DispatchRequest{Agent: "general-purpose", Prompt: "start background job"})
		done <- dispatchOutcome{result, err}
	}()

	select {
	case <-toolEnded:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("worker bash tool did not finish")
	}
	pidPath := filepath.Join(cwd, "worker.pid")
	workerPID := readJobPID(t, pidPath)
	t.Cleanup(func() { _ = syscall.Kill(workerPID, syscall.SIGTERM) })
	cancel()
	select {
	case outcome := <-done:
		if outcome.err != nil || outcome.result.Text == "too late" {
			t.Fatalf("cancelled Dispatch: %+v, %v", outcome.result, outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled Dispatch did not return")
	}
	if !waitJobGone(workerPID, 2*time.Second) {
		t.Fatalf("worker background process %d survived cancelled Dispatch", workerPID)
	}
}

func TestDispatchNestedWorkerJobScopeIsIndependent(t *testing.T) {
	script := `models:
  faux-1:
    - tool_call: {name: bash, args: {command: "sleep 30 & echo $! > outer.pid"}, id: outer_bash}
    - on_tool_result: outer_bash
      then:
        - tool_call: {name: task, args: {subagent_type: general-purpose, prompt: "start inner", model: fast}, id: inner_task}
    - on_tool_result: inner_task
      then:
        - tool_call: {name: bash, args: {command: "kill -0 $(cat outer.pid) && echo alive > outer_alive"}, id: outer_check}
    - on_tool_result: outer_check
      then:
        - text: "outer done"
  faux-2:
    - tool_call: {name: bash, args: {command: "sleep 30 & echo $! > inner.pid"}, id: inner_bash}
    - on_tool_result: inner_bash
      then:
        - text: "inner done"
`
	d, parentSession, cwd := newParentAndDispatcher(t, script, nil)
	t.Cleanup(func() { _ = parentSession.Harness.Close() })
	d.Roles = map[string]string{"fast": "faux/faux-2"}
	type innerReturnState struct {
		innerRunning bool
		outerRunning bool
	}
	innerReturned := make(chan innerReturnState, 1)
	d.OnEvent = func(ev SubagentEvent) {
		if ev.Kind == SubagentEventDone && ev.Depth == 2 {
			innerPID := readJobPID(t, filepath.Join(cwd, "inner.pid"))
			outerPID := readJobPID(t, filepath.Join(cwd, "outer.pid"))
			// Signal delivery is asynchronous; allow the killed process to
			// leave the run queue before testing sibling survival.
			innerReturned <- innerReturnState{!waitJobGone(innerPID, 2*time.Second), jobRunning(outerPID)}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := d.Dispatch(ctx, DispatchRequest{Agent: "general-purpose", Prompt: "start outer"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "outer done" {
		t.Fatalf("outer report = %q", result.Text)
	}
	outerPID := readJobPID(t, filepath.Join(cwd, "outer.pid"))
	innerPID := readJobPID(t, filepath.Join(cwd, "inner.pid"))
	t.Cleanup(func() {
		_ = syscall.Kill(outerPID, syscall.SIGTERM)
		_ = syscall.Kill(innerPID, syscall.SIGTERM)
	})
	select {
	case state := <-innerReturned:
		if state.innerRunning || !state.outerRunning {
			t.Fatalf("at inner return: inner running=%v, outer running=%v", state.innerRunning, state.outerRunning)
		}
	default:
		t.Fatal("nested worker did not report completion")
	}
	if _, err := os.Stat(filepath.Join(cwd, "outer_alive")); err != nil {
		t.Fatalf("inner worker cleanup stopped outer worker job: %v", err)
	}
	if !waitJobGone(innerPID, 2*time.Second) || !waitJobGone(outerPID, 2*time.Second) {
		t.Fatalf("nested worker job survived Dispatch: inner=%v outer=%v", jobRunning(innerPID), jobRunning(outerPID))
	}
}

func workerBackgroundScript(pidFile string) string {
	return fmt.Sprintf(`model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "sleep 30 & echo $! > %s"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "done"
`, pidFile)
}

func readJobPID(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid job PID %q: %v", raw, err)
	}
	return pid
}

func jobRunning(pid int) bool {
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	state := strings.TrimSpace(string(out))
	return state != "" && !strings.HasPrefix(state, "Z")
}

func waitJobGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for jobRunning(pid) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}
