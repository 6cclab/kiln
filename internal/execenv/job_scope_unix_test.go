//go:build unix

package execenv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func scopedSleep(t *testing.T, env *Env) string {
	t.Helper()
	marker := fmt.Sprintf("%09d", time.Now().UnixNano()%1_000_000_000)
	command := fmt.Sprintf("sleep 30.%s & echo started", marker)
	result, err := env.Exec(context.Background(), command, ExecOptions{})
	if err != nil || !strings.Contains(result.Text, "started") {
		t.Fatalf("start background job: result=%+v err=%v", result, err)
	}
	if !scopedSleepSurvives(marker) {
		t.Fatalf("background job %s already exited", marker)
	}
	return marker
}

func scopedSleepSurvives(marker string) bool {
	return exec.Command("pgrep", "-f", "sleep 30[.]"+marker).Run() == nil
}

func waitScopedSleepGone(t *testing.T, marker string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for scopedSleepSurvives(marker) {
		if time.Now().After(deadline) {
			t.Fatalf("background job %s survived scope close", marker)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestJobScopeStopsDescendantsAfterShellExit(t *testing.T) {
	env := New(t.TempDir())
	env.JobScope = NewJobScope()
	t.Cleanup(env.JobScope.Close)
	marker := scopedSleep(t, env)
	env.JobScope.Close()
	waitScopedSleepGone(t, marker)
	if _, err := env.Exec(context.Background(), "echo late", ExecOptions{}); !errors.Is(err, ErrJobScopeClosed) {
		t.Fatalf("late Exec error = %v, want ErrJobScopeClosed", err)
	}
}

func TestJobScopeCloseStopsActiveCommand(t *testing.T) {
	env := New(t.TempDir())
	env.JobScope = NewJobScope()
	t.Cleanup(env.JobScope.Close)
	marker := fmt.Sprintf("%09d", time.Now().UnixNano()%1_000_000_000)
	command := fmt.Sprintf("sleep 30.%s & echo started; wait", marker)
	done := make(chan error, 1)
	go func() {
		_, err := env.Exec(context.Background(), command, ExecOptions{})
		done <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for !scopedSleepSurvives(marker) {
		if time.Now().After(deadline) {
			t.Fatal("active scoped command did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	env.JobScope.Close()
	select {
	case err := <-done:
		if !errors.Is(err, ErrJobScopeClosed) {
			t.Fatalf("active Exec error = %v, want ErrJobScopeClosed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("active Exec did not return after scope close")
	}
	waitScopedSleepGone(t, marker)
}

func TestJobScopeDoesNotStopSiblingOrSessionJobs(t *testing.T) {
	first, sibling, root := New(t.TempDir()), New(t.TempDir()), New(t.TempDir())
	first.JobScope, sibling.JobScope = NewJobScope(), NewJobScope()
	defer first.JobScope.Close()
	defer sibling.JobScope.Close()
	defer KillLeftoverJobs()
	a, b, c := scopedSleep(t, first), scopedSleep(t, sibling), scopedSleep(t, root)
	first.JobScope.Close()
	waitScopedSleepGone(t, a)
	if !scopedSleepSurvives(b) || !scopedSleepSurvives(c) {
		t.Fatal("closing first scope stopped a sibling or session job")
	}
	sibling.JobScope.Close()
	waitScopedSleepGone(t, b)
	if !scopedSleepSurvives(c) {
		t.Fatal("closing sibling scope stopped a session job")
	}
	KillLeftoverJobs()
	waitScopedSleepGone(t, c)
}

func TestJobScopeCloseRacesStartsAndIsIdempotent(t *testing.T) {
	env := New(t.TempDir())
	env.JobScope = NewJobScope()
	t.Cleanup(env.JobScope.Close)
	// Hold one process active so every closer races a real registered
	// group, even if the remaining goroutines have not been scheduled.
	active := make(chan error, 1)
	go func() {
		_, err := env.Exec(context.Background(), "echo ready > ready; sleep 0.3", ExecOptions{})
		active <- err
	}()
	ready := filepath.Join(env.Cwd, "ready")
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("active scoped command did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := env.Exec(context.Background(), "sleep 0.03", ExecOptions{})
			if err != nil && !errors.Is(err, ErrJobScopeClosed) {
				t.Errorf("concurrent Exec: %v", err)
			}
		}()
	}
	var closers sync.WaitGroup
	for i := 0; i < 3; i++ {
		closers.Add(1)
		go func() { defer closers.Done(); env.JobScope.Close() }()
	}
	wg.Wait()
	closers.Wait()
	select {
	case err := <-active:
		if err != nil && !errors.Is(err, ErrJobScopeClosed) {
			t.Fatalf("active Exec error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("active command did not return")
	}
	late := filepath.Join(env.Cwd, "late")
	if _, err := env.Exec(context.Background(), "echo escaped > late", ExecOptions{}); !errors.Is(err, ErrJobScopeClosed) {
		t.Fatalf("late Exec error = %v", err)
	}
	if _, err := os.Stat(late); !os.IsNotExist(err) {
		t.Fatalf("late command ran: %v", err)
	}
}

func TestJobScopeQuietFinishAndFailedStart(t *testing.T) {
	closedZero := &JobScope{}
	closedZero.Close()
	closedEnv := New(t.TempDir())
	closedEnv.JobScope = closedZero
	if _, err := closedEnv.Exec(context.Background(), "true", ExecOptions{}); !errors.Is(err, ErrJobScopeClosed) {
		t.Fatalf("zero-value scope closed before start: %v", err)
	}

	// The zero value is usable and exercises lazy initialization too.
	scope := &JobScope{}
	env := New(t.TempDir())
	env.JobScope = scope
	t.Cleanup(scope.Close)
	if err := scope.start(exec.Command(filepath.Join(env.Cwd, "missing"))); err == nil {
		t.Fatal("missing executable started")
	}
	for i := 0; i < 3; i++ {
		if _, err := env.Exec(context.Background(), "true", ExecOptions{}); err != nil {
			t.Fatalf("quiet command %d: %v", i, err)
		}
	}
	scope.mu.Lock()
	groups, active := len(scope.groups), scope.active
	scope.mu.Unlock()
	if groups != 0 || active != 0 {
		t.Fatalf("quiet commands left %d groups and %d active calls", groups, active)
	}
	scope.Close()
	var nilScope *JobScope
	nilScope.Close()
}

func TestJobScopeContextCancellationPreservesScope(t *testing.T) {
	env := New(t.TempDir())
	env.JobScope = NewJobScope()
	t.Cleanup(env.JobScope.Close)
	marker := fmt.Sprintf("%09d", time.Now().UnixNano()%1_000_000_000)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := env.Exec(ctx, fmt.Sprintf("sleep 30.%s & wait", marker), ExecOptions{})
		done <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for !scopedSleepSurvives(marker) {
		if time.Now().After(deadline) {
			t.Fatal("scoped command did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Exec error = %v, want context cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled Exec did not return")
	}
	waitScopedSleepGone(t, marker)
	if _, err := env.Exec(context.Background(), "true", ExecOptions{}); err != nil {
		t.Fatalf("scope unusable after one command cancelled: %v", err)
	}
}
