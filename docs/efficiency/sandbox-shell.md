# Isolated worker shell confinement

## Foundation retained

`enableWorkerShell` builds a mandatory macOS Seatbelt manager for a linked child worktree. Its direct live Bash-tool test permits child and private-temp writes and rejects writes to the parent checkout, shared Git metadata and shared temp, plus a Unix socket connection. The helper requires descriptor-confined files and a worker `JobScope`, disables unsandboxed commands and proxy access, and rejects Linux and Windows. These are profile and helper tests, not proof of safe product activation.

## Product activation is refused

`orchestration.isolatedShell` remains explicit and default off, and requires `isolatedWorkers`. Dispatch now refuses it before starting a child on every platform with an error explaining that detached descendants cannot yet be owned safely across process-group changes. The default file-only isolated worker remains functional. A nonempty isolated-worker tool allowlist that names an unsupported tool fails explicitly instead of widening to Bash. Parent MCP and host-executing extension tools remain unavailable.

The old dispatch activation call was removed. Dispatch retains its ordinary
parent-sandbox conflict check and removes Bash from every isolated-worker
active tool set as a second boundary behind the explicit refusal.

The reason is concrete: a child of Bash can call `setsid()`, detach from the registered process group, and outlive `JobScope.Close()` and worktree cleanup. The read-only review probe `TestReviewConfinedDispatchDetachedDescendant` observed an exact live PID with PPID 1 after actual dispatched-child cleanup; the reviewer killed that PID. `internal/execenv/job_scope.go` currently owns process groups, not descendants that leave them. A best-effort scan of PIDs or a poll after the fact would not establish ownership across rapid detach and reparenting, and risks killing an unrelated reused PID. The macOS SDK's `sys/event.h` states that kqueue `NOTE_TRACK`, `NOTE_TRACKERR` and `NOTE_CHILD` have been unsupported since 10.5; this precludes treating those flags as a descendant tracker. Apple's [kqueue manual](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/kqueue.2.html) documents `NOTE_FORK` as a notification, not automatic descendant ownership.

The shared parent gate now checks final Bash arguments in a child tool wrapper after all before-tool rewrites, before execution. The wrapper applies to ordinary child Bash, which remains usable. Isolated Bash would use the same wrapper if robust process containment is implemented later; currently it never starts.

## Evidence and limits

- `TestDispatchIsolatedShellRefusesDetachedDescendants` covers normal and already-canceled dispatch attempts with a scripted detaching command; neither starts the child. `TestWorkerShellNoOptInLeavesEnvironmentUnchanged` keeps default file-only behavior.
- Temporarily removing the fail-closed check made both refusal subtests fail. The red result is `/private/tmp/kiln-wiring-round2-safety-revert-red.log`; the restored focused pass is `/private/tmp/kiln-wiring-round2-safety-green.log`.
- `TestDispatchChildBashGatesFinalRewrittenArgs` covers a plugin-style rewrite of `echo safe` to a parent-denied `printf forbidden > child-file` through ordinary dispatched Bash. The final gate rejects it. The prior isolated-path version failed before the fix and its log is `/private/tmp/kiln-wiring-round2-gate-allowlist-red.log`.
- Temporarily removing the final Bash wrapper made the ordinary child regression fail because the rewritten command executed. That red result is also in `/private/tmp/kiln-wiring-round2-safety-revert-red.log`.
- `TestDispatchIsolatedWorkerRejectsUnsupportedToolAllowlist` failed before the explicit allowlist check and passed afterward; logs are `/private/tmp/kiln-wiring-round2-gate-allowlist-red.log` and `/private/tmp/kiln-wiring-round2-gate-allowlist-green.log`.
- Its Bash-specific subtest exposed a remaining silent acceptance in file-only mode, recorded in `/private/tmp/kiln-wiring-round2-allowlist-bash-red.log`. Bash is now removed before allowlist validation, and all three unsupported-list cases pass in `/private/tmp/kiln-wiring-round2-allowlist-bash-green.log`.
- The direct `TestWorkerShellLiveSeatbeltThroughBashTool` remains a profile regression. It does not cover descendant cleanup and does not make the product mode supported.

General-purpose shell-capable isolated workers remain incomplete on macOS, Linux and Windows. File-only isolation and explicit patch review remain available. The existing process-group scope still stops jobs that stay in their original group; it is not a robust container or descendant lifetime owner.
