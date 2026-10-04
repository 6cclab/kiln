package cli

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/gitfiles"
)

// kiln runs git itself for the status line (branch, dirty), on a
// repository that sandboxed commands may have written to. Git can run
// commands the repository names: an fsmonitor, hooks, and the clean
// filters .gitattributes selects. So kiln's git:
//
//   - always runs with core.fsmonitor and hooks off and without taking
//     optional locks;
//   - runs inside the session's sandbox whenever one is active, so a
//     filter or anything else git spawns is sandboxed like the commands
//     that could have planted it.

// gitSandbox is the session's sandbox for kiln's own git calls, set by
// startSandbox; nil runs git directly.
var gitSandbox atomic.Pointer[execenv.CommandSandbox]

func setGitSandbox(sb execenv.CommandSandbox) {
	if sb == nil {
		gitSandbox.Store(nil)
		return
	}
	gitSandbox.Store(&sb)
}

// kilnGitArgs puts git's hardening options before args.
func kilnGitArgs(args []string) []string {
	return append([]string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null"}, args...)
}

func runKilnGit(ctx context.Context, cwd string, args ...string) (string, error) {
	argv := kilnGitArgs(args)
	var cmd *exec.Cmd
	if p := gitSandbox.Load(); p != nil {
		quoted := make([]string, 0, len(argv)+1)
		quoted = append(quoted, "git")
		for _, a := range argv {
			quoted = append(quoted, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
		}
		w, err := (*p).Wrap("/bin/sh", strings.Join(quoted, " "), cwd)
		if err != nil {
			return "", err
		}
		if w.Cleanup != nil {
			defer w.Cleanup()
		}
		cmd = exec.CommandContext(ctx, w.Argv[0], w.Argv[1:]...)
		drop := map[string]bool{}
		for _, k := range w.Unset {
			drop[k] = true
		}
		for k := range w.Env {
			drop[k] = true
		}
		var env []string
		for _, kv := range os.Environ() {
			if k, _, _ := strings.Cut(kv, "="); !drop[k] {
				env = append(env, kv)
			}
		}
		for k, v := range w.Env {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
	} else {
		cmd = exec.CommandContext(ctx, "git", argv...)
	}
	cmd.Dir = cwd
	out, err := cmd.Output()
	return string(out), err
}

// gitBranchFromFiles is the branch cwd's repository is on, read from
// .git's files without running git (internal/gitfiles): what kiln shows
// before the folder is trusted. ok is false outside a repository.
func gitBranchFromFiles(cwd string) (string, bool) {
	r, ok := gitfiles.Find(cwd)
	if !ok {
		return "", false
	}
	branch, _, ok := r.Branch()
	return branch, ok
}
