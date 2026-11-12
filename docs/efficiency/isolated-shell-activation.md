# Isolated worker shell activation

Status: `orchestration.isolatedShell` works on Linux with bubblewrap. It is refused, with an explicit error, on macOS and Windows. It stays user-only and off by default.

## Why only Linux

A worker's Bash is safe to enable only if kiln can stop every process it started before the worktree is removed. `JobScope.Close` kills process groups, and a descendant that calls `setsid()` leaves its group.

| Platform | Descendant ownership | Result |
| --- | --- | --- |
| Linux | `bwrap --unshare-pid --die-with-parent`: the command runs in a PID namespace. When its init exits (the command ends, or `JobScope.Close` kills the bwrap group) the kernel kills every process in the namespace, `setsid()` ones included. | enabled |
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

- Teardown after `JobScope.Close` kills the outer bwrap is performed by the kernel (the namespace init receives its parent-death signal). It is not synchronous with `Close` returning; the tests poll for up to two seconds, and kiln itself does not wait for it. A process still being torn down could in theory hold a file in the worktree for a few milliseconds when cleanup starts.
- A socket created inside `/etc` or the Git metadata after the per-command scan, by another host process while the command runs, is reachable. Nothing else outside the allowlist is.
- The Git metadata, including the parent's `config`, hooks and packed refs, is readable by the worker. Reads of the parent worktree files are not possible.
- The container runs were as root; non-root user-namespace behaviour on a host with restricted unprivileged user namespaces is reported as unavailable (refusal), not tested here.
- macOS and Windows remain refused.
