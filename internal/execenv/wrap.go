package execenv

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// Sandbox decides, per command, whether a shell command runs inside an OS
// sandbox (internal/sandbox) and how. Env.Sandbox carries it; the bash
// tools consult it, the user's own "!" commands do not (Claude Code runs
// those outside the sandbox).
type Sandbox interface {
	// Active reports that commands are being sandboxed at all.
	Active() bool
	// ForCommand returns how command runs, or nil to run it unsandboxed.
	// disable is the model's dangerouslyDisableSandbox request.
	ForCommand(command string, disable bool) CommandSandbox
	// OffersUnsandboxed reports whether the bash tool offers the
	// dangerouslyDisableSandbox parameter at all.
	OffersUnsandboxed() bool
}

// CommandSandbox wraps one command.
type CommandSandbox interface {
	// Wrap returns what runs `shell -c command` in cwd inside the
	// sandbox.
	Wrap(shell, command, cwd string) (Wrapped, error)
	// Explain returns a note for the model about a failed sandboxed run
	// (what the sandbox refused, how to retry), or "".
	Explain(output string, exitCode int) string
}

// Wrapped is a sandboxed command line: the argv to run instead of
// `shell -c command`, environment variables to set and to remove, and a
// cleanup to run once the command has exited.
type Wrapped struct {
	Argv    []string
	Env     map[string]string
	Unset   []string
	Cleanup func()
	// ExtraFiles are inherited by the command as fds 3, 4, ... Exec closes
	// its own copies once the command has started (or failed to start).
	ExtraFiles []*os.File
	// Started, if set, runs right after the command started.
	Started func()
	// Drain, if set, runs after the command's process has exited, before
	// the job scope is told the command finished. It returns an error when
	// it cannot confirm that every process the command started has stopped
	// (a sandbox that owns a PID namespace waits for it to be empty); Exec
	// then reports that error.
	Drain func(*os.ProcessState) error
}

// shellCommand builds the process Exec starts: `shell -c command`, or
// what opts.Sandbox wraps it in.
func shellCommand(ctx context.Context, shellPath, command, cwd string, opts ExecOptions) (*exec.Cmd, func(), Wrapped, error) {
	env := buildEnv(opts.Env, opts.InheritEnv)
	if opts.Sandbox == nil {
		cmd := exec.CommandContext(ctx, shellPath, "-c", command)
		cmd.Dir = cwd
		cmd.Env = env
		return cmd, func() {}, Wrapped{}, nil
	}
	w, err := opts.Sandbox.Wrap(shellPath, command, cwd)
	if err != nil {
		return nil, nil, Wrapped{}, err
	}
	cmd := exec.CommandContext(ctx, w.Argv[0], w.Argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = applyEnv(env, w.Env, w.Unset)
	cmd.ExtraFiles = w.ExtraFiles
	cleanup := w.Cleanup
	if cleanup == nil {
		cleanup = func() {}
	}
	return cmd, cleanup, w, nil
}

// applyEnv returns base with set's variables replacing any of the same
// name and unset's removed.
func applyEnv(base []string, set map[string]string, unset []string) []string {
	drop := map[string]bool{}
	for _, k := range unset {
		drop[k] = true
	}
	for k := range set {
		drop[k] = true
	}
	out := make([]string, 0, len(base)+len(set))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			out = append(out, kv)
		}
	}
	for k, v := range set {
		out = append(out, k+"="+v)
	}
	return out
}
