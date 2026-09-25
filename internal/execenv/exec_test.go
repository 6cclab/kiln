package execenv

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestExecBasic(t *testing.T) {
	env := New(t.TempDir())
	result, err := env.Exec(context.Background(), "echo hello", ExecOptions{InheritEnv: true})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", result.ExitCode)
	}
	if strings.TrimSpace(result.Text) != "hello" {
		t.Fatalf("text = %q, want %q", result.Text, "hello")
	}
}

func TestExecExitCode(t *testing.T) {
	env := New(t.TempDir())
	result, err := env.Exec(context.Background(), "exit 7", ExecOptions{InheritEnv: true})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if result.ExitCode != 7 {
		t.Fatalf("exitCode = %d, want 7", result.ExitCode)
	}
}

func TestExecStreamsUpdates(t *testing.T) {
	env := New(t.TempDir())
	var kinds []UpdateKind
	// Two short bursts with a pause between them, together smaller than
	// the retained tail (10 lines, 200 bytes). The pause guarantees two
	// reads; staying inside the window guarantees the second snapshot is
	// the first plus a tail, so it is an append. Under load a single long
	// burst arrived in one read (one replace), and two long bursts were
	// disjoint tails (two replaces): both were spurious failures.
	_, err := env.Exec(context.Background(), `for i in 1 2 3; do echo "line $i"; done; sleep 0.2; for i in 4 5 6; do echo "line $i"; done`, ExecOptions{
		InheritEnv: true,
		Capture:    CaptureLimits{MaxBytes: 200, MaxLines: 10, Retain: RetainTail},
		OnUpdate: func(u ShellOutputUpdate) {
			kinds = append(kinds, u.Kind)
		},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if len(kinds) == 0 {
		t.Fatal("expected streamed updates")
	}
	sawSlideOrAppend := false
	for _, k := range kinds {
		if k == UpdateSlide || k == UpdateAppend {
			sawSlideOrAppend = true
		}
	}
	if !sawSlideOrAppend {
		t.Errorf("expected at least one append or slide update, got %v", kinds)
	}
}

func TestExecCaptureLimitsTruncateTail(t *testing.T) {
	env := New(t.TempDir())
	result, err := env.Exec(context.Background(), `for i in $(seq 1 100); do echo "line $i"; done`, ExecOptions{
		InheritEnv: true,
		Capture:    CaptureLimits{MaxBytes: 4096, MaxLines: 10, Retain: RetainTail},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !result.Truncation.Truncated {
		t.Fatal("expected truncation with maxLines=10 against 100 lines of output")
	}
	if result.Truncation.TotalLines != 100 {
		t.Fatalf("TotalLines = %d, want 100", result.Truncation.TotalLines)
	}
	lines := strings.Split(strings.TrimRight(result.Text, "\n"), "\n")
	if len(lines) != 10 {
		t.Fatalf("expected 10 retained lines, got %d: %q", len(lines), result.Text)
	}
	if lines[len(lines)-1] != "line 100" {
		t.Fatalf("expected tail retention to keep the last line, got %q", lines[len(lines)-1])
	}
}

// sleepSurvives reports whether a "sleep" process tagged with marker is
// still alive, using pgrep -f against the full command line the way the
// task asks: the marker is a unique argument baked into the sleep
// invocation itself so this can't match an unrelated sleep on a shared
// test machine.
func sleepSurvives(marker string) bool {
	err := exec.Command("pgrep", "-f", "sleep 3[.]"+marker).Run()
	return err == nil // exit 0 means pgrep found a match
}

func TestExecTimeoutKillsProcessGroup(t *testing.T) {
	env := New(t.TempDir())
	// A fractional suffix: the sleep stays a valid ~3s sleep, and the
	// pgrep pattern below cannot match an unrelated "sleep 30" from another
	// process on the machine (a three-digit integer marker did, when it
	// happened to be 0).
	marker := fmt.Sprintf("%09d", time.Now().UnixNano()%1_000_000_000)
	command := fmt.Sprintf(`sleep 3.%s & wait`, marker)

	_, err := env.Exec(context.Background(), command, ExecOptions{
		InheritEnv: true,
		Timeout:    200 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected a timeout error")
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !sleepSurvives(marker) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("background sleep 3.%s survived Exec timeout", marker)
}

func TestExecContextCancelKillsProcessGroup(t *testing.T) {
	env := New(t.TempDir())
	marker := fmt.Sprintf("%09d", time.Now().UnixNano()%1_000_000_000+1)
	command := fmt.Sprintf(`sleep 3.%s & wait`, marker)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = env.Exec(ctx, command, ExecOptions{InheritEnv: true})
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !sleepSurvives(marker) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("background sleep 3.%s survived context cancellation", marker)
}
