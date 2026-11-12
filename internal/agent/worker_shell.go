package agent

import (
	"errors"
	"fmt"
	"runtime"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/sandbox"
)

// isolatedShellSupport reports whether kiln can guarantee that every process a
// worker's Bash starts is stopped before the worktree is removed. File and
// socket confinement alone is not enough: a descendant that calls setsid()
// leaves the process group JobScope kills. Linux bubblewrap runs commands in a
// PID namespace, which ends every process in it when its init exits, so it
// qualifies. macOS has no equivalent (docs/efficiency/isolated-shell-activation.md)
// and Windows has no worker sandbox.
func isolatedShellSupport(goos string) error {
	switch goos {
	case "linux":
		return nil
	case "darwin":
		return errors.New("isolated shell is unavailable on macOS: it has no PID namespace or other mechanism that stops a descendant that calls setsid() before the worktree is removed")
	default:
		return fmt.Errorf("isolated shell is unavailable on %s: no worktree-aware command sandbox", goos)
	}
}

// enableWorkerShellForOS installs a mandatory worktree-specific command
// sandbox; callers pass runtime.GOOS as goos. Its cleanup must run after
// JobScope.Close and before the worktree is removed. A nil cleanup is returned
// on failure so dispatch cannot start the worker.
func enableWorkerShellForOS(workerEnv, parentEnv *execenv.Env, worktree, goos string) (func(), error) {
	if (goos != "darwin" && goos != "linux") || goos != runtime.GOOS {
		return nil, fmt.Errorf("isolated worker shell requires macOS Seatbelt or Linux bubblewrap; %s has neither", goos)
	}
	if workerEnv == nil || parentEnv == nil || !workerEnv.ConfinedFilesEnabled() || workerEnv.JobScope == nil {
		return nil, errors.New("isolated worker shell requires confined files and a worker job scope")
	}
	var parentConfig sandbox.Config
	if parentEnv.Sandbox != nil {
		parentManager, ok := parentEnv.Sandbox.(*sandbox.Manager)
		if !ok || parentManager == nil {
			return nil, errors.New("isolated worker shell cannot inherit an unknown parent sandbox")
		}
		parentConfig = parentManager.Config()
	}
	manager, err := sandbox.NewWorker(parentConfig, sandbox.Options{}, parentEnv.Cwd, worktree)
	if err != nil {
		return nil, fmt.Errorf("prepare isolated worker shell sandbox: %w", err)
	}
	workerEnv.Sandbox = manager
	return manager.Close, nil
}
