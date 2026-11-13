# Core runtime readiness fixes

This change repairs crash recovery in ordinary coding sessions and makes print
mode stop when an incomplete operation cannot be resumed. It follows the
read-only readiness audit at HEAD
`5a7150f` and does not implement optional efficiency controls.

## What changed

- A saved `outcome_ready` tool payload is committed to the branch without
  running the tool again. An already committed matching result is reused. A
  missing or invalid pending payload becomes one paired error result and a
  failed operation; a conflicting committed result fails explicitly without
  duplicating its entry.
- An `effect_pending` tool is never automatically replayed: it may already
  have changed files or external state before the process died. Recovery
  writes a paired error result that says the effect is unknown, skips later
  unstarted calls, and ends failed. The person can inspect the effect and
  decide whether to retry. A canceled recovery records `aborted` and pairs
  pending calls with canceled/error results.
- Intermediate tool results keep the whole batch in `AtTools` on disk. Only
  the final result advances to `AtCheckpoint`. For older checkpoint records,
  recovery reconstructs missing calls from the assistant entry and branch
  results. It runs remaining calls only when the old runtime proves they were
  sequential built-in calls; missing plugin or concurrent calls are treated
  as uncertain.
- Print mode propagates a failed `ResumeIncomplete` result and exits before
  sending the next prompt. The two real-binary crash-resume tests now assert
  the explicit uncertain result, no second append to a marker file, and a
  usable TUI prompt for a new user turn.
- After a conflicting committed result, `Prompt` checks the stored branch
  before appending a new user entry or requesting a model. The check rejects
  missing, extra, duplicate, or mismatched tool results and survives a JSONL
  reopen. Manual compaction uses the same guard before its summarizer call.
  The existing `/rewind` path can select a valid ancestor, or the
  person can start a new session. The corrupt entries stay available for
  inspection and are never sent to a provider.

## Regression evidence

The tests were run against the original production files through a temporary
Go `-overlay`, leaving the working tree's tests in place. The final red run is
`/private/tmp/kiln-readiness-red-final.log`; it shows failures in
`TestReadinessResumeMaterializesOutcomeReady`,
`TestReadinessResumeMissingOrInvalidSavedResult`,
`TestReadinessResumeCompletesMultiToolBatch`,
`TestReadinessResumeRepairsLegacyCheckpointGap`,
`TestReadinessLegacyConcurrentGapDoesNotReplayUnknownEffect`,
`TestReadinessResumeCancellationIsAborted`,
`TestReadinessResumeDoesNotDuplicateInFlightEffect`,
`TestReadinessResumeConflictingCommittedResultFailsClosed`, and
`TestPrintResumeFailureStopsBeforeNewPrompt`. The revised real-binary tests
also fail against the original production files when `GOFLAGS` passes the
same overlay to the `go build` in `TestMain`
(`/private/tmp/kiln-readiness-e2e-red.log`):
`TestSession_CrashMidToolThenResume` returned success instead of an uncertain
failure, and `TestTUI_Continue_AfterKilledPrintRun` waited for approval to
replay the command instead of showing uncertainty.

The harness regressions create real JSONL transactions, close the store, and
reopen it before `Resume`, so they test crash-boundary state from disk. The
batch test invokes production commit methods to create the intermediate
state. The malformed payload case uses valid JSON with an invalid payload
shape, which can be committed through the JSONL store.
`TestReadinessResumeAlreadyCommittedResultIsNotDuplicated` and
`TestReadinessResumeCancellationDuringToolIsAborted` add defensive coverage. They
also passed on the original code, so they are not claimed as independently red
regressions.

Green verification after the implementation:

- `go test ./internal/harness ./internal/cli -count=1` passed.
- The two revised crash-resume end-to-end tests passed against a freshly
  built binary.
- `make check` passed with `GOCACHE=/private/tmp/kiln-go-cache` after the final
  edits. The sandboxed final attempt could not resolve the pinned staticcheck
  dependency, so the same gate was rerun with network access and passed. A first run
  with `GOTMPDIR=/private/tmp` failed only because that override changed the
  input to `TestCacheEnvRespectsUserSettings`; the clean-environment rerun
  passed. Full logs: `/private/tmp/kiln-readiness-make-check.log`,
  `/private/tmp/kiln-readiness-make-check-cleanenv.log`, and
  `/private/tmp/kiln-readiness-make-check-final.log`.
- `make e2e` passed after updating its two stale replay expectations;
  `test/e2e` completed in 227.544 seconds. Full log:
  `/private/tmp/kiln-readiness-make-e2e-final.log`.

No paid/live model calls or real-home settings writes were made.

## Review fix: persistent provider transcript integrity

The follow-up review found that `Resume` correctly failed on a conflicting
committed result but then cleared the current operation, allowing a later
normal `Prompt` to send the malformed branch to a provider. The new branch
preflight runs both before a user entry is committed and at the start of the
drive loop, before routing or automatic compaction can make a provider call.
Manual `CompactWith` also checks before selecting a summarizer.
It reads the branch from storage each time, so a restart does not remove the
guard. Navigating to an earlier valid tip restores ordinary prompting without
rewriting or hiding the bad entries.

`TestReadinessConflictBlocksLaterPromptUntilRewind` and
`TestReadinessWrongToolNameBlocksLaterPrompt` each failed against the prior
`turn.go` through a temporary Go overlay: the later `Prompt` completed instead
of stopping (`/private/tmp/kiln-conflict-prompt-red-final.log`). The first
test reopens real JSONL after recovery, checks that no user entry or provider
request is added, rewinds to the assistant entry's valid parent, then proves
a new prompt succeeds. The second checks a conflicting tool name.
`TestReadinessValidToolHistoryAllowsLaterPromptAfterReopen` passed both before
and after the fix, confirming the normal saved-result path remains usable.
`TestReadinessConflictBlocksManualCompaction` failed before its guard with
`requests=1` on the corrupt branch (`/private/tmp/kiln-conflict-compact-red.log`)
and passes with zero requests after the fix. Existing
`TestCompactionWritesEntry` and `TestManualCompactSummarisesRecentTurns` pass
on valid branches. `TestReadinessCorruptCheckpointNeverRequestsModel` failed
against the prior `turn.go` with `requests=1` and passes with zero requests
after the drive guard (`/private/tmp/kiln-conflict-checkpoint-red.log`).
`make check` passed after the final code changes
(`/private/tmp/kiln-conflict-make-check-final.log`), and the fresh full
`make e2e` passed in 224.413 seconds
(`/private/tmp/kiln-conflict-make-e2e-final.log`).

The first full e2e attempt in this round failed only
`TestTUI_MCP_BackgroundConnect`: its fixture MCP server was unavailable in
that run (`/private/tmp/kiln-conflict-make-e2e.log`). The isolated test passed
three times, and a full rerun passed before the manual-compaction guard was
added (`/private/tmp/kiln-conflict-mcp-targeted.log` and
`/private/tmp/kiln-conflict-make-e2e-rerun.log`). No MCP code was changed.

## Remaining limits

A crash after an external effect but before its durable outcome is inherently
ambiguous; recovery intentionally stops and asks the person to inspect it.
Older checkpoint records for plugin or concurrent calls lack enough state to
prove whether a missing call ran, so they are not replayed. A conflicting
already-committed entry is treated as session corruption and stops recovery;
prompting on that branch now fails, while rewinding to a valid ancestor or
starting a fresh session remains available. JSONL storage
appends transactions without `fsync`, so a power loss can still erase the last
transaction.

The visible recovery message follows the handoff's red error block and tool
error rows without changing the renderer. A real-terminal scenario to add is:
kill a scratch-home print run after its bash marker effect, launch
`kiln --continue`, inspect the uncertain tool/error blocks and idle editor
with no approval prompt, then submit a new user turn. The revised PTY test
covers that path automatically; no real-terminal screenshot drive was run in
this branch.

The coordinator's baseline `make check` once failed in
`TestKillLeavesNoProcessBehind`; ten isolated repeats and two full agent
package runs in the audit, and this branch's clean `make check`, passed.
The root cause of that intermittent process-cleanup failure is unconfirmed and
still under separate investigation. No `internal/agent` or `internal/execenv`
change was made.
