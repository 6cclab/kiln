# Progress-loop detection

This change adds `runtimeEfficiency.progressLoopLimit`, an opt-in integer from 2 to 10. It defaults to zero. A user setting can enable it; repository settings can only turn it off or lower an already enabled limit. The guard runs within one operation and makes no model calls of its own.

At the limit, the harness commits one bounded corrective user message. If the same kind of loop reaches the limit again in that operation, it uses the existing progress-guard failure path. Calls emitted after the triggering call in the same assistant message are paired with truthful skipped results, so their effects do not run. Memory stays bounded to 40 read fingerprints, 40 file-state fingerprints, and fixed counters; content is not retained.

The detector covers identical successful reads with unchanged result content; successful read-only sequences of two through four calls with unchanged results; and edit/write calls that restore a prior state of a regular file inside the working directory. The file check reads at most 1 MiB before and after each edit/write. Conservatively parsed read-only Bash calls also participate in fixed sequences when a bounded snapshot of the worktree is complete. Each snapshot covers at most 256 paths and 1 MiB of regular-file content, excluding Git metadata. Directory enumeration reads at most the remaining path budget plus one entry before sorting, so a very wide directory becomes unknown without loading every name. A changed snapshot resets the sequence. An unreadable, linked or oversized tree is unknown and cannot trigger a notice. Failed calls continue through the existing failure guards.

## Evidence

- `TestProgressLoopRepeatedSuccessfulReadAndFileChange` verifies the repeated read threshold and that changed content resets it.
- `TestProgressLoopRepeatedReadOnlySequence` verifies a two-call cycle.
- `TestProgressLoopEditRevertRestoresFileState` verifies an actual file restored by two mutations.
- `TestProgressLoopDefaultOff` and `TestProgressLoopLimitDefaultsOffAndProjectCannotEnable` verify opt-in and user opt-out behavior.
- `TestProgressLoopNoticeThenEscalationInTurn` verifies one committed notice followed by a failed operation after the loop continues.
- `TestProgressLoopSuccessfulBashSequenceObservesWorkspace` and `TestProgressLoopSuccessfulBashSequenceInTurn` verify unchanged Bash sequences, file changes and a committed notice in a real operation; both failed before Bash sequence support in `/private/tmp/kiln-wiring-round2-progress-red.log` and passed afterward in `/private/tmp/kiln-wiring-round2-progress-green.log`. `TestProgressLoopBashUnknownStateDoesNotTrigger` verifies bounded observation fails closed. `TestProgressLoopWideDirectoryStopsAtEnumerationBound` failed to compile before the bounded directory reader existed (`/private/tmp/kiln-wiring-round2-wide-red.log`) and passes with a 1,024-entry directory (`/private/tmp/kiln-wiring-round2-wide-green.log`).
- With the guard temporarily disabled, the first three shape tests failed; restoring it made them pass.
- `make e2e` passed. `make check` passed after a separate run; an earlier concurrent run hit `TestKillShellLeavesNoProcessBehind` while e2e was active, and that test passed alone and in the final full check.

## Nested workers and provenance

The CLI forwards the effective limit into the root dispatcher, which passes it
to each child agent and nested dispatcher. `TestDispatchNestedWorkerInheritsProgressLoopLimit`
checks that a grandchild's repeated successful reads commit a corrective notice.
The efficiency report includes the effective value and per-field source.

## Remaining gaps

- Detection deliberately excludes unknown or effectful tool sequences, including effectful Bash commands whose external effects cannot be observed, and files outside the working directory, symlinks, and files over 1 MiB. The read-only sequence check observes tool results and bounded worktree state for Bash; it does not claim unchanged external task state or Git metadata. A before-tool hook that rewrites an edit/write path is not currently tracked as a file cycle.
