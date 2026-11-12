package agent

import (
	"errors"
	"fmt"
	"runtime"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/sandbox"
)

// enableWorkerShell installs a mandatory worktree-specific command sandbox.
// Its cleanup must run after JobScope.Close and before the worktree is removed.
// A nil cleanup is returned on failure so dispatch cannot start the worker.
func enableWorkerShell(workerEnv, parentEnv *execenv.Env, worktree string) (func(), error) {
	return enableWorkerShellForOS(workerEnv, parentEnv, worktree, runtime.GOOS)
}

func enableWorkerShellForOS(workerEnv, parentEnv *execenv.Env, worktree, goos string) (func(), error) {
	if goos != "darwin" || runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("isolated worker shell requires macOS Seatbelt; %s cannot confine Unix sockets and files", goos)
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
