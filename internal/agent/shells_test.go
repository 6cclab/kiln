package agent

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/tools"
)

// Background shells run real processes. These are not mocked: the update
// protocol (replace/append/slide) is exactly the part that is easy to get
// wrong, and a mock would encode the same misunderstanding as the code,
// mirroring test/background-shell.test.ts's own reasoning.

// testWait is how long a test waits for an event it expects. It only bounds a
// hang: every wait returns the moment its condition holds, so a slow machine
// costs time, never a failure.
const testWait = 30 * time.Second

// waitFinished blocks until shell id's command has exited and its output is
// fully absorbed.
func waitFinished(t *testing.T, shells *BackgroundShells, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	if finished, ok := shells.Wait(ctx, id); !ok || !finished {
		t.Fatalf("shell %s did not finish (known=%v)", id, ok)
	}
}

// waitFor polls cond until it holds, failing after testWait.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestStartReturnsImmediately(t *testing.T) {
	env := execenv.New(t.TempDir())
	shells := NewBackgroundShells()
	// The command outlives the test, so Start returning at all (rather than
	// after the command) is the proof; no elapsed-time bound is needed.
	shell, err := shells.Start(context.Background(), "sleep 120", env)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if shell.Status != ShellRunning {
		t.Fatalf("status = %q, want running", shell.Status)
	}
	shells.Kill(shell.ID)
}

// TestRunningCountAndOnChange guards the status-line indicator's two needs:
// Running() must count only shells still running, and OnChange must fire on
// every lifecycle transition (start, exit, kill) so the footer refreshes even
// while the user is idle.
func TestRunningCountAndOnChange(t *testing.T) {
	env := execenv.New(t.TempDir())
	shells := NewBackgroundShells()

	var changes atomic.Int32
	shells.SetOnChange(func() { changes.Add(1) })

	if got := shells.Running(); got != 0 {
		t.Fatalf("Running() on an empty registry = %d, want 0", got)
	}

	longRunner, err := shells.Start(context.Background(), "sleep 5", env)
	if err != nil {
		t.Fatalf("Start long: %v", err)
	}
	quick, err := shells.Start(context.Background(), "exit 0", env)
	if err != nil {
		t.Fatalf("Start quick: %v", err)
	}

	// The quick shell exits on its own; the long one keeps running.
	waitFinished(t, shells, quick.ID)
	if got := shells.Running(); got != 1 {
		t.Fatalf("Running() after one exit = %d, want 1", got)
	}

	// OnChange fired at least for: 2 starts + the quick shell's exit.
	if got := changes.Load(); got < 3 {
		t.Fatalf("OnChange fired %d times, want >=3 (2 starts + 1 exit)", got)
	}

	before := changes.Load()
	shells.Kill(longRunner.ID)
	if got := shells.Running(); got != 0 {
		t.Fatalf("Running() after kill = %d, want 0", got)
	}
	if changes.Load() <= before {
		t.Fatalf("OnChange did not fire on kill (stayed at %d)", before)
	}

	// Killing an already-dead shell must not fire OnChange again.
	steady := changes.Load()
	shells.Kill(quick.ID)
	if changes.Load() != steady {
		t.Fatalf("OnChange fired on killing an already-finished shell")
	}
}

func TestReadIncrementallyNeverRepeats(t *testing.T) {
	env := execenv.New(t.TempDir())
	shells := NewBackgroundShells()
	shell, err := shells.Start(context.Background(), "for i in 1 2 3 4; do echo tick-$i; sleep 0.25; done", env)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// First read: whatever has arrived once there is anything. Second read:
	// everything else, after the shell has finished.
	var first []string
	waitFor(t, "first output", func() bool {
		lines, _, _, ok := shells.Read(shell.ID)
		if !ok {
			t.Fatal("Read: not found")
		}
		first = append(first, lines...)
		return len(first) > 0
	})

	waitFinished(t, shells, shell.ID)
	second, _, _, _ := shells.Read(shell.ID)
	seen := map[string]bool{}
	for _, l := range first {
		seen[l] = true
	}
	for _, l := range second {
		if seen[l] {
			t.Fatalf("%q was returned twice", l)
		}
	}

	third, _, _, _ := shells.Read(shell.ID)
	if len(third) != 0 {
		t.Fatalf("a read with nothing new returned content: %v", third)
	}

	all := append(append([]string{}, first...), second...)
	want := []string{"tick-1", "tick-2", "tick-3", "tick-4"}
	if fmt.Sprint(all) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", all, want)
	}
}

func TestRecordsExitCode(t *testing.T) {
	env := execenv.New(t.TempDir())
	shells := NewBackgroundShells()
	shell, err := shells.Start(context.Background(), "exit 3", env)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFinished(t, shells, shell.ID)
	after, ok := shells.Get(shell.ID)
	if !ok {
		t.Fatal("Get: not found")
	}
	if after.Status != ShellExited {
		t.Fatalf("status = %q, want exited", after.Status)
	}
	if after.ExitCode == nil || *after.ExitCode != 3 {
		t.Fatalf("exitCode = %v, want 3", after.ExitCode)
	}
}

func TestKillsAProcessThatWouldRunForever(t *testing.T) {
	env := execenv.New(t.TempDir())
	shells := NewBackgroundShells()
	shell, err := shells.Start(context.Background(), "while true; do echo alive; sleep 0.2; done", env)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	running, _ := shells.Get(shell.ID)
	if running.Status != ShellRunning {
		t.Fatalf("status = %q, want running", running.Status)
	}

	shells.Kill(shell.ID)
	killed, _ := shells.Get(shell.ID)
	if killed.Status != ShellKilled {
		t.Fatalf("status = %q, want killed", killed.Status)
	}

	// Once Exec has returned the process is gone and its output absorbed:
	// drain it, then check nothing more shows up.
	waitFinished(t, shells, shell.ID)
	shells.Read(shell.ID)
	time.Sleep(600 * time.Millisecond) // negative wait: slow only passes vacuously
	lines, _, _, _ := shells.Read(shell.ID)
	if len(lines) != 0 {
		t.Fatalf("output kept arriving after the kill: %v", lines)
	}
}

func TestLeavesAnAlreadyExitedShellAlone(t *testing.T) {
	env := execenv.New(t.TempDir())
	shells := NewBackgroundShells()
	shell, err := shells.Start(context.Background(), "echo done", env)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFinished(t, shells, shell.ID)
	after, ok := shells.Kill(shell.ID)
	if !ok {
		t.Fatal("Kill: not found")
	}
	if after.Status != ShellExited {
		t.Fatalf("a finished shell was marked %q, want exited", after.Status)
	}
}

func TestKillAllStopsEverythingRunning(t *testing.T) {
	// A dev server outliving the session is a port held by a process the
	// user has no handle on.
	env := execenv.New(t.TempDir())
	shells := NewBackgroundShells()
	a, err := shells.Start(context.Background(), "sleep 10", env)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	b, err := shells.Start(context.Background(), "sleep 10", env)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	shells.KillAll()

	as, _ := shells.Get(a.ID)
	bs, _ := shells.Get(b.ID)
	if as.Status != ShellKilled {
		t.Fatalf("a.status = %q, want killed", as.Status)
	}
	if bs.Status != ShellKilled {
		t.Fatalf("b.status = %q, want killed", bs.Status)
	}
}

func TestReportsAnUnknownIDRatherThanPanicking(t *testing.T) {
	shells := NewBackgroundShells()
	if _, _, _, ok := shells.Read("nope"); ok {
		t.Fatal("Read of unknown id reported ok")
	}
	if _, ok := shells.Kill("nope"); ok {
		t.Fatal("Kill of unknown id reported ok")
	}
	if _, ok := shells.Get("nope"); ok {
		t.Fatal("Get of unknown id reported ok")
	}
}

func TestRenderShellListEmpty(t *testing.T) {
	if got := RenderShellList(nil); got != "No background shells." {
		t.Fatalf("got %q", got)
	}
}

func TestRenderShellListShowsRunningAndFinished(t *testing.T) {
	env := execenv.New(t.TempDir())
	shells := NewBackgroundShells()
	if _, err := shells.Start(context.Background(), "echo one", env); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFinished(t, shells, "bash_1")
	out := RenderShellList(shells.List())
	if !strings.Contains(out, "echo one") {
		t.Fatalf("missing command in %q", out)
	}
	if !strings.Contains(out, "bash_1") {
		t.Fatalf("missing id in %q", out)
	}
}

// --- brief-specific additions beyond the ported TS suite ---

func TestBufferHoldsLast500LinesOf1000(t *testing.T) {
	env := execenv.New(t.TempDir())
	shells := NewBackgroundShells()
	shell, err := shells.Start(context.Background(), "for i in $(seq 1 1000); do echo line-$i; done", env)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitFinished(t, shells, shell.ID)

	lines, _, snap, ok := shells.Read(shell.ID)
	if !ok {
		t.Fatal("Read: not found")
	}
	if len(lines) != tools.MaxBackgroundShellLines {
		t.Fatalf("got %d lines, want %d", len(lines), tools.MaxBackgroundShellLines)
	}
	if snap.Dropped != 1000-tools.MaxBackgroundShellLines {
		t.Fatalf("dropped = %d, want %d", snap.Dropped, 1000-tools.MaxBackgroundShellLines)
	}
	if lines[0] != "line-501" {
		t.Fatalf("first retained line = %q, want line-501", lines[0])
	}
	if lines[len(lines)-1] != "line-1000" {
		t.Fatalf("last retained line = %q, want line-1000", lines[len(lines)-1])
	}
}

func TestIncrementalReadsReturnDisjointSlices(t *testing.T) {
	env := execenv.New(t.TempDir())
	shells := NewBackgroundShells()
	shell, err := shells.Start(context.Background(), "for i in 1 2 3 4 5; do echo n$i; sleep 0.15; done", env)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	seen := map[string]bool{}
	deadline := time.Now().Add(testWait)
	for time.Now().Before(deadline) {
		lines, _, snap, ok := shells.Read(shell.ID)
		if !ok {
			t.Fatal("Read: not found")
		}
		for _, l := range lines {
			if seen[l] {
				t.Fatalf("%q read twice across incremental reads", l)
			}
			seen[l] = true
		}
		if snap.Status != ShellRunning {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(seen) != 5 {
		t.Fatalf("saw %d distinct lines, want 5: %v", len(seen), seen)
	}
}

func TestReaderWhoWaitsUntilEvictionGetsMissed(t *testing.T) {
	env := execenv.New(t.TempDir())
	shells := NewBackgroundShells()
	shell, err := shells.Start(context.Background(), "echo first-line", env)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFinished(t, shells, shell.ID)
	// Advance the cursor past the one line this shell has produced so far.
	if _, _, _, ok := shells.Read(shell.ID); !ok {
		t.Fatal("Read: not found")
	}

	second, err := shells.Start(context.Background(), "for i in $(seq 1 1000); do echo flood-$i; done", env)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFinished(t, shells, second.ID)

	// second's cursor is still 0: it never read anything, and 1000 lines
	// flowed through a 500-line buffer, so everything before the retained
	// window is "missed".
	_, missed, _, ok := shells.Read(second.ID)
	if !ok {
		t.Fatal("Read: not found")
	}
	if missed <= 0 {
		t.Fatalf("missed = %d, want > 0", missed)
	}
}

func TestKillLeavesNoProcessBehind(t *testing.T) {
	env := execenv.New(t.TempDir())
	shells := NewBackgroundShells()
	marker := strconv.FormatInt(time.Now().UnixNano()%100000, 10)
	command := "sleep 30" + marker
	shell, err := shells.Start(context.Background(), command, env)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, "process "+command+" to start", func() bool { return processMatching(command) })

	shells.Kill(shell.ID)

	waitFor(t, "process "+command+" to die after Kill", func() bool { return !processMatching(command) })
}

func processMatching(pattern string) bool {
	return exec.Command("pgrep", "-f", pattern).Run() == nil
}

func TestSlideAcrossExecEnvCaptureWindow(t *testing.T) {
	// Forces execenv's own OutputCapture to slide (its default window is
	// 2000 lines / 50KB) on top of this registry's own 500-line cap having
	// already trimmed the copy shorter than the source's tracked view -
	// the scenario absorb's Drop-clamp exists for. The assertion is just
	// "no panic, and the tail survives correctly".
	env := execenv.New(t.TempDir())
	shells := NewBackgroundShells()
	shell, err := shells.Start(context.Background(), "for i in $(seq 1 3000); do echo line-$i; done", env)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitFinished(t, shells, shell.ID)

	lines, _, snap, ok := shells.Read(shell.ID)
	if !ok {
		t.Fatal("Read: not found")
	}
	if len(lines) != tools.MaxBackgroundShellLines {
		t.Fatalf("got %d lines, want %d", len(lines), tools.MaxBackgroundShellLines)
	}
	if lines[len(lines)-1] != "line-3000" {
		t.Fatalf("last line = %q, want line-3000", lines[len(lines)-1])
	}
	if snap.Dropped < 2500 {
		t.Fatalf("dropped = %d, want at least 2500", snap.Dropped)
	}
}
