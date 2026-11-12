# Isolated worker shell activation

Status: `orchestration.isolatedShell` works on Linux with bubblewrap. It is refused, with an explicit error, on macOS and Windows. It stays user-only and off by default.

## Why only Linux

A worker's Bash is safe to enable only if kiln can stop every process it started before the worktree is removed. `JobScope.Close` kills process groups, and a descendant that calls `setsid()` leaves its group.

| Platform | Descendant ownership | Result |
| --- | --- | --- |
| Linux | `bwrap --unshare-pid --die-with-parent`: the command runs in a PID namespace. When its init exits (the command ends, or `JobScope.Close` kills the bwrap group) the kernel kills every process in the namespace, `setsid()` ones included, and kiln waits on a pidfd of the init until that has finished (Synchronous teardown, below). | enabled |
| macOS | None found (below). | refused |
| Windows | No worktree-aware command sandbox. | refused |

`isolatedShellSupport` (`internal/agent/worker_shell.go`) is the single gate; dispatch calls it before creating a worktree.

## What changed

- `internal/sandbox/bwrap.go`: a plan with `PrivateRoot` (set for Linux workers in `Manager.plan`) does not bind the host `/`. bwrap's empty tmpfs is the root, remounted read-only, and only these paths exist in it:
  - `/usr /bin /sbin /lib* /etc` read-only (`workerSystemDirs`; symlinks such as `/bin -> usr/bin` are recreated);
  - the child worktree and a private temp directory, writable;
  - the worktree's Git metadata (`gitDirs`), read-only.

  Rules about paths that are not visible (parent checkout write denials, deny-read entries for the parent) are dropped rather than mounted, because mounting them would expose the path.
- Unix sockets (spec item c). Avoiding the host `/` removes `/run`, `/var/run`, `/tmp`, `/mnt` and `$HOME`, where sockets live, so `docker.sock`, the D-Bus and ssh-agent sockets and anything in `/tmp` do not exist for the worker. The two visible places that can hold sockets are scanned each command (`workerHiddenSockets`) and any socket found is covered by `/dev/null`: `/etc`, and the worktree's Git metadata (git's fsmonitor daemon listens in the git directory). Abstract sockets and TCP are cut off by `--unshare-net`. This is stricter than masking known paths: nothing outside the allowlist is reachable, whatever its name.
- Writes (spec items a, b): only the child worktree and the private temp are writable; `.git` and the shared Git directories are read-only; the parent checkout is not mounted. `rejectWorkerHardlinks` now runs on Linux too (it was macOS only), because an inode shared with the parent checkout is the same file wherever it is mounted.
- Dispatch: the blanket refusal is replaced by `isolatedShellSupport`. On a supported platform dispatch builds the worker sandbox after the worker's `JobScope` exists and releases it after `JobScope.Close` and before the worktree is removed. Bash stays in the worker's tool set only when the opt-in is on.
- Settings: `orchestration.isolatedShell: true` is honoured only from user-scope settings, like `autoIntegrate`. A repository or `--settings` file cannot turn it on (`internal/claude/settings/settings.go`); it can still turn it off.

## Behaviour a user will notice

- The worker sees only the system toolchain directories. Tools installed under `$HOME` or `/opt` (a Go or Rust toolchain in the home directory, say) are not found; `/usr/local` is visible. `/tmp` is read-only; `$TMPDIR` is a private writable directory.
- A background process started by a command ends when the command ends (the namespace's init exits). `nohup x &` does not survive the command.
- The worker has no network, not even loopback to the host.
- bwrap and socat must be installed and user namespaces allowed; otherwise `NewWorker` fails and dispatch returns that error (fail closed).

## macOS: why it stays refused

Evidence gathered on macOS 27.0.1 with `sandbox-exec`:

1. A Seatbelt `(deny syscall-unix (syscall-number 147 82))` does block `setsid` and `setpgid` for the process and its descendants. A Python probe got `EPERM` for both.
2. It does not keep descendants in the group. `os.posix_spawn(..., setpgroup=0)` under the same profile created a child in its own process group (child pgid 20852, parent pgid 20848). The kernel applies `POSIX_SPAWN_SETPGROUP` and `POSIX_SPAWN_SETSID` inside `posix_spawn`, not through the filtered syscalls.
3. Denying `posix_spawn` as well (`147 82 244`) closes that, but breaks ordinary tools. `/usr/bin/python3` (the Xcode shim) fails to start under that profile: `python3: posix_spawn: ... Undefined error: 0`. Rust's std, libuv (Node) and libc `system()` also spawn through `posix_spawn` on macOS (stated from knowledge of those libraries, not tested here). A shell mode in which those fail is not usable, and the profile would depend on a syscall-number filter whose completeness cannot be checked from user space.
4. kqueue `NOTE_TRACK` is unsupported since macOS 10.5 (see `sandbox-shell.md`). macOS has no PID namespaces or cgroups, and process coalitions need private entitlements. Scanning for PIDs after the fact is excluded by the roadmap, because rapid detach and PID reuse make it unsound.

Probe files were in a scratch directory and are not part of the change. The Seatbelt profile and its live test (`worker_shell_darwin_test.go`) are kept as a foundation; `TestDispatchIsolatedShellRefusesDetachedDescendants` still asserts that dispatch refuses before running anything.

## Synchronous teardown

A command's namespace is confirmed empty before its `Exec` returns, so `JobScope.Close` and the worktree removal never race a live process.

Mechanism (`internal/sandbox/nswatch_linux.go`):

1. Every worker command is started with `bwrap --info-fd 3`. bwrap writes `{"child-pid": N}` (the host PID of the namespace's init) before it runs the command. Exec passes the pipe through `Wrapped.ExtraFiles`.
2. As soon as the command starts, a goroutine reads that JSON and calls `pidfd_open(N)`. A pidfd names that process for good, so a recycled PID cannot be mistaken for it; it also works on a process that is not our child, which the init is not once its parent bwrap has been killed. The goroutine checks `/proc/N/status` shows `NSpid` ending in 1 (an init), and treats `ESRCH` as already exited.
3. After `cmd.Wait` returns (normal exit, timeout, cancel or `JobScope.Close`), Exec calls `Drain`, which polls the pidfd for readability, bounded by 5 seconds. Only then does the command count as finished for the job scope (`execFinished`), so `Close` returns after it.
4. `Manager.Shutdown`, which dispatch uses as the worker's cleanup, waits again on any namespace not yet confirmed (a command still in flight when `Close` gave up after its own 3 seconds). If anything is unconfirmed it returns an error, and dispatch leaves the worktree on disk (`releaseWorkerWorktree`) and returns that error with the dispatch result. Nothing falls back to asynchronous teardown.
5. `NewWorker` probes the same path once (a real bwrap run with `--info-fd` and a pidfd on its init). A kernel without `pidfd_open` (before 5.3) or a bwrap without `--info-fd` makes worker creation fail with an error naming pidfd, so isolated shell is refused.

Why a readable pidfd means the namespace is empty (Linux v6.6 source):

- `kernel/exit.c` `do_exit` -> `exit_notify` -> `forget_original_parent` -> `find_child_reaper`, which for the namespace's init calls `zap_pid_ns_processes(pid_ns)`.
- `kernel/pid_namespace.c` `zap_pid_ns_processes` marks the namespace as not accepting new processes, sends SIGKILL to every other task in it, reaps its children, and then loops in `TASK_INTERRUPTIBLE` sleep until `pid_ns->pid_allocated` is down to the init's own PID; the comment says `free_pid()` wakes it once the other tasks are gone. The init therefore cannot finish exiting while any task of the namespace exists.
- `exit_notify` sets `exit_state` and calls `do_notify_parent`, and the pidfd's wait queue is woken by `do_notify_pidfd` at the top of `do_notify_parent` (`kernel/signal.c`). That happens after `forget_original_parent` returned, that is after `zap_pid_ns_processes`. `pidfd_poll` reports `EPOLLIN` when the thread group has exited (`exit_state` set); I recalled that function from memory, the fetch of `kernel/pid.c` did not contain it, so that last link is not verified against source in this session. `pid_namespaces(7)` states the kill-on-init-exit behaviour.
- The fetched source was read through a text summarizer, not a full checkout. The empirical tests below back the chain.

Evidence (container on a 7.0.14-linuxkit kernel, bubblewrap 0.12.0, `--privileged` and `--init`):

- `TestWorkerLinuxScopeCloseWaitsForSlowTeardown` runs a `setsid` descendant holding a large amount of memory, so its exit after SIGKILL is slow, and takes one look right after `JobScope.Close`. With the wait it passes (Close took about 16 ms, three runs). With `Wrapped.Drain` disabled it fails three of three (Close took under 0.5 ms and the descendant was still there). This is the red/green pair for the wait.
- The ordinary `setsid sleep` tests (`TestWorkerLinuxDetachedDescendantDiesWithCommand`, `...WithScope`, and the two dispatch tests) now check once, without polling, and loop 20 times. They pass with the wait. They also passed with the wait disabled (measured): a plain `sleep` dies faster than Exec's own return path, so they do not demonstrate the race; the slow-teardown test does.
- `TestNSWatchTimeoutIsAnError`: an injected pidfd for a process that never exits makes the wait return a "still running" error after the 150 ms test timeout, and `Shutdown` reports it. `TestReleaseWorkerWorktreeKeepsWorktreeWhenShellNotStopped`: with that error the worktree stays on disk and the error wraps the cause; with no error it is removed.
- `TestNSWatchNoInfo`: bwrap killed before reporting is an error; bwrap that exited by itself without a namespace is not.
- `TestWorkerLinuxRefusedWithoutPidfd`: an opener returning `ENOSYS` makes `NewWorker` fail with an error naming pidfd.

## Evidence

Linux tests ran in a disposable `golang:1.26.6` container (linux/arm64) with bubblewrap 0.12.0 and socat installed, started with `--privileged`. With only `--security-opt seccomp=unconfined apparmor=unconfined` bwrap failed with `Creating new namespace failed`; adding `--cap-add SYS_ADMIN` failed with `loopback: Failed RTM_NEWADDR`; `--privileged` worked. The container ran as root. The copy of the repo was `rsync`ed in without `.git` and no HOME or config was mounted.

| Test | Proves |
| --- | --- |
| `TestWorkerLinuxDetachedDescendantDiesWithCommand` | a `setsid` background `sleep` is gone after the command returns |
| `TestWorkerLinuxDetachedDescendantDiesWithScope` | the same descendant is gone after `JobScope.Close` while the command is still running |
| `TestWorkerLinuxHidesHostUnixSockets` | a socket in `/tmp` and one in the Git directory are unreachable; the control (ordinary sandbox) reaches the same `/tmp` socket |
| `TestWorkerLinuxFilesystem` | child and temp writable; parent, `.git`, `/etc` not; `git status` works; hardlink to a parent file is refused |
| `TestWorkerLinuxNetworkIsolated` | a host loopback listener is unreachable |
| `TestBwrapPrivateRootMountsOnlyAllowlist`, `TestWorkerHiddenSocketsFindsMetadataSockets` | argv has no host `/` mount and nothing outside the allowlist (run on any OS) |
| `TestDispatchIsolatedShellRunsAndOwnsDetachedDescendants`, `TestDispatchIsolatedShellCancelStopsDetachedDescendants` | through `Dispatch`: Bash runs in the worker, the patch contains only the worker's file, nothing leaks to the parent, no descendant survives completion or cancel |
| `TestIsolatedShellRequiresUserScopeOptIn` | repository and extra settings cannot enable the setting |

Red runs: removing `--unshare-pid` made both descendant tests (sandbox and dispatch) fail with `a setsid descendant outlived ...`; setting `PrivateRoot=false` made the socket test fail (`worker reached host socket`) and the filesystem test fail (parent file readable); removing the settings strip failed the settings test.

## Not covered

- A bwrap killed in the few microseconds between its `clone` and its `--info-fd` write leaves a namespace kiln never learned of. Exec treats "killed, no info" as unconfirmed, so the worktree is kept (a spurious keep when the kill landed before the clone, never a spurious removal).
- A worktree kept after a failed wait is not removed by kiln later; it needs manual cleanup (`git worktree remove --force`).
- A socket created inside `/etc` or the Git metadata after the per-command scan, by another host process while the command runs, is reachable. Nothing else outside the allowlist is.
- The Git metadata, including the parent's `config`, hooks and packed refs, is readable by the worker. Reads of the parent worktree files are not possible.
- The container runs were as root; non-root user-namespace behaviour on a host with restricted unprivileged user namespaces is reported as unavailable (refusal), not tested here.
- macOS and Windows remain refused.
