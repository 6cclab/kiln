package execenv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ExecOptions controls Exec, mirroring pi's ShellExecOptions.
type ExecOptions struct {
	// Cwd is the working directory for the command. Relative paths resolve
	// against Env.Cwd. Defaults to Env.Cwd.
	Cwd string
	// Env are extra environment variables for the command.
	Env map[string]string
	// InheritEnv includes the current process's environment. Defaults to
	// true.
	InheritEnv bool
	// Timeout bounds the command's runtime. Zero means no timeout.
	Timeout time.Duration
	// Capture bounds retained output. Capture is required for OnUpdate to
	// receive anything; without it output is discarded except for the
	// final ExecResult.Text, which is still bounded by DefaultMaxBytes/
	// DefaultMaxLines.
	Capture CaptureLimits
	// Spill preserves the complete output on disk once the limits are
	// crossed, recording the path in ExecResult.SpillPath.
	Spill bool
	// OnUpdate is called with every incremental output change.
	OnUpdate func(ShellOutputUpdate)
}

// ExecResult is the outcome of one Exec call, mirroring pi's
// ShellExecResult plus the bounded text view (pi delivers text only
// through OnUpdate; this port also returns the final view directly since
// most callers just want the answer).
type ExecResult struct {
	ExitCode   int
	TimedOut   bool
	Text       string
	Truncation TruncationResult
	SpillPath  string
	// JobsLeft reports that the command left background jobs running
	// ("server &"): its process group outlived the shell. They are
	// stopped by KillLeftoverJobs when the session ends.
	JobsLeft bool
}

// leftoverGroups holds the process groups of commands whose background
// jobs outlived them, so the session can stop them on exit instead of
// leaving a server holding a port nobody has a handle on.
var leftoverGroups = struct {
	mu    sync.Mutex
	pgids map[int]struct{}
}{pgids: map[int]struct{}{}}

// KillLeftoverJobs sends SIGTERM to every process group a command left
// running (ExecResult.JobsLeft). Called on exit.
func KillLeftoverJobs() {
	leftoverGroups.mu.Lock()
	defer leftoverGroups.mu.Unlock()
	for pgid := range leftoverGroups.pgids {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		delete(leftoverGroups.pgids, pgid)
	}
}

// resolveShell picks the shell Exec runs commands under, mirroring
// getShellConfig in nodejs.js: an explicit ShellPath if set, else
// /bin/bash if present, else the first "bash" on PATH, else "sh". Unlike
// pi, this port only targets Unix (darwin/linux), so the Windows Git-Bash
// search is not ported.
func resolveShell(shellPath string) (string, error) {
	if shellPath != "" {
		if _, err := os.Stat(shellPath); err == nil {
			return shellPath, nil
		}
		return "", fmt.Errorf("custom shell path not found: %s", shellPath)
	}
	if _, err := os.Stat("/bin/bash"); err == nil {
		return "/bin/bash", nil
	}
	if path, err := exec.LookPath("bash"); err == nil {
		return path, nil
	}
	if path, err := exec.LookPath("sh"); err == nil {
		return path, nil
	}
	return "/bin/sh", nil
}

// pipeDrainDelay is how long Exec keeps reading output after the shell
// exits, for pipes a background job still holds open.
const pipeDrainDelay = time.Second

// feedWriter adapts Exec's feed callback to an io.Writer.
type feedWriter func(string)

func (f feedWriter) Write(p []byte) (int, error) {
	f(string(p))
	return len(p), nil
}

// Exec runs command under a shell (`<shell> -c <command>`), streaming
// bounded output through opts.OnUpdate and returning the final bounded
// view. The child runs in its own process group (Setpgid); cancelling ctx
// or crossing opts.Timeout kills the whole group, not just the immediate
// child, so background jobs the command spawned (`sleep 30 &`) die too.
func (e *Env) Exec(ctx context.Context, command string, opts ExecOptions) (ExecResult, error) {
	if err := ctx.Err(); err != nil {
		return ExecResult{}, err
	}
	shellPath, err := resolveShell(e.ShellPath)
	if err != nil {
		return ExecResult{}, err
	}
	cwd := e.Cwd
	if opts.Cwd != "" {
		cwd = e.AbsolutePath(opts.Cwd)
	}
	if _, err := os.Stat(cwd); err != nil {
		return ExecResult{}, fmt.Errorf("working directory does not exist: %s", cwd)
	}

	cmd := exec.CommandContext(ctx, shellPath, "-c", command)
	cmd.Dir = cwd
	cmd.Env = buildEnv(opts.Env, opts.InheritEnv)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// exec.CommandContext's default cancel (ctx.Done) sends the process a
	// plain Kill signal to the leader only; override so the whole process
	// group dies, matching pi's killProcessTree.
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}

	capture := NewOutputCapture(opts.Capture, opts.OnUpdate)
	var (
		mu          sync.Mutex
		spillFile   *os.File
		spillPath   string
		spillPrefix []string
	)
	feed := func(chunk string) {
		mu.Lock()
		defer mu.Unlock()
		wasTruncated := capture.Truncated()
		capture.Push(chunk)
		if !opts.Spill || chunk == "" {
			return
		}
		switch {
		case spillFile != nil:
			_, _ = spillFile.WriteString(chunk)
		case wasTruncated:
			startSpill(&spillFile, &spillPath, capture, chunk)
		case capture.Truncated():
			for _, prefix := range spillPrefix {
				startSpill(&spillFile, &spillPath, capture, prefix)
			}
			spillPrefix = nil
			startSpill(&spillFile, &spillPath, capture, chunk)
		default:
			spillPrefix = append(spillPrefix, chunk)
		}
	}

	// Output goes through writers, not StdoutPipe, so Wait owns the copy
	// and WaitDelay can bound it: a job the command backgrounded
	// ("server &") inherits the pipes and holds them open after the shell
	// exits, and reading to EOF would then wait for that job forever. Once
	// the shell has exited, Wait gives the pipes pipeDrainDelay to drain,
	// then closes them and returns; the job keeps running.
	cmd.Stdout = feedWriter(feed)
	cmd.Stderr = feedWriter(feed)
	cmd.WaitDelay = pipeDrainDelay

	if err := cmd.Start(); err != nil {
		return ExecResult{}, err
	}

	// Armed only once Start has returned: the callback runs on the timer's
	// goroutine and reads the process it kills, so arming it earlier raced
	// with Start's own write of cmd.Process (seen under -race). The flag is
	// atomic for the same reason: Stop does not synchronize with a callback
	// that is already running.
	var timedOut atomic.Bool
	var timer *time.Timer
	if opts.Timeout > 0 {
		pid := cmd.Process.Pid
		timer = time.AfterFunc(opts.Timeout, func() {
			timedOut.Store(true)
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		})
	}

	waitErr := cmd.Wait()
	if errors.Is(waitErr, exec.ErrWaitDelay) {
		waitErr = nil // the shell exited 0; only a background job held the pipes
	}
	// The shell ran in its own process group; if anything is still in it,
	// the command backgrounded a job that is still running.
	pgid := cmd.Process.Pid
	jobsLeft := syscall.Kill(-pgid, 0) == nil
	if jobsLeft {
		leftoverGroups.mu.Lock()
		leftoverGroups.pgids[pgid] = struct{}{}
		leftoverGroups.mu.Unlock()
	}
	if timer != nil {
		timer.Stop()
	}

	mu.Lock()
	if spillFile != nil {
		_ = spillFile.Close()
	}
	final := capture.Snapshot()
	mu.Unlock()

	if timedOut.Load() {
		return ExecResult{TimedOut: true, Text: final.Text, Truncation: final.Truncation, SpillPath: final.SpillPath},
			fmt.Errorf("timeout after %s", opts.Timeout)
	}
	if ctx.Err() != nil {
		return ExecResult{Text: final.Text, Truncation: final.Truncation, SpillPath: final.SpillPath}, ctx.Err()
	}

	exitCode := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
			if exitCode < 0 {
				// Killed by a signal with no conventional exit code: map to
				// 128+signal so callers do not mistake it for success,
				// mirroring nodejs.js.
				if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
					exitCode = 128 + int(status.Signal())
				} else {
					exitCode = 1
				}
			}
		} else {
			return ExecResult{}, waitErr
		}
	}

	return ExecResult{
		ExitCode:   exitCode,
		Text:       final.Text,
		Truncation: final.Truncation,
		SpillPath:  final.SpillPath,
		JobsLeft:   jobsLeft,
	}, nil
}

func buildEnv(extra map[string]string, inherit bool) []string {
	var out []string
	if inherit {
		out = append(out, os.Environ()...)
	}
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

// startSpill lazily creates the temp file that preserves complete output
// once the bounded view has been truncated, mirroring nodejs.js's
// startSpill/spillPrefix dance (minus its backpressure handling, which
// exists there only to pause a live child process — synchronous file
// writes need no such pausing here).
func startSpill(fileRef **os.File, pathRef *string, capture *OutputCapture, chunk string) {
	if *fileRef == nil {
		f, err := os.CreateTemp("", "pi-output-*.log")
		if err != nil {
			return
		}
		*fileRef = f
		*pathRef = f.Name()
		capture.SetSpillPath(*pathRef)
	}
	_, _ = (*fileRef).WriteString(chunk)
}
