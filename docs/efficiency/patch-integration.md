# Automatic isolated worker patch integration

## Behavior

Automatic integration remains off by default. It requires the user's own
`orchestration.autoIntegrate` opt-in and isolated workers; repository settings
cannot grant it. Explicit artifact application remains available.

The harness makes one decision after **every** tool result from an assistant
response has been committed, including ordinary tools after a `task` call.
Any failed worker or ordinary tool blocks the whole batch. Successful worker
artifacts pass through the existing batch preflight and single apply, which
rechecks the shared baseline, manifests, digests, paths, and overlaps under
the repository lock. A conflict or moved baseline applies no patch and names
the retained manifests and the validation failure, including all overlapping
paths reported by preflight.

Before apply, the session commits an integration intent keyed by the assistant
message. The integration outcome, a branch note, and the operation checkpoint
then commit together. On restart, a recorded intent is reported as **uncertain**
and is never applied again automatically: the parent checkout and retained
manifests need inspection. A recovered batch with no intent is left for explicit
handling with a durable notice listing its manifests. This conservative rule
covers a stop before apply, after apply, and before the outcome commit without
guessing whether an external Git effect happened. An already committed outcome
is replayed as a status event if the process stopped before reporting it.

The branch note is sent to the next model request, rendered in session history
and the TUI, and printed by the CLI independently of what the model says.
Text and JSON print results include the notice; stream JSON emits a
`worker_integration` event and includes notices in its final result.

## Evidence

- `TestRunWorkerBatchAutomaticIntegration`: disjoint apply, default off,
  project-only opt-in rejection, overlap, worker failure, later parent write,
  and later ordinary-tool failure. It asserts checkout files, CLI stdout,
  persisted JSONL note, and the next model request against a faux provider.
- `TestWorkerBatchCompletionBlocksMixedFailureAndMovedBaseline` checks
  all-or-none behavior when a later tool fails or the baseline moves.
- `TestWorkerBatchCompletionRestartWindowsNeverReplayPatch` closes and reopens
  JSONL storage before deciding recovered batches with no intent, an intent
  before apply, and an intent after apply.
- `TestWorkerIntegrationResumeAfterResultBeforeDecision` reopens a pending
  harness operation at before-apply and after-apply-before-report windows. It
  checks the explicit uncertain notice and that no effect is replayed.
- `TestWorkerIntegrationResumeAfterDurableOutcomeBeforeEvent` reopens at
  checkpoint, assistant-ready, and assistant-effect-pending states and checks
  the status event and one durable note.
- `TestWorkerIntegrationCarriesOutcomeIntoNextRequestState` checks that the
  durable decision ID survives into the next request's pending state.
- `TestCollectorWorkerIntegrationVisibleWithoutModelNarration`,
  `TestBridgeWorkerIntegrationCommitsVisibleNoteWithoutSyntheticCopy`, and
  `TestWorkerIntegrationNoticeReplaysAsSystemNote` cover print, live TUI event,
  and TUI session replay surfaces.

Regression reversals were run by temporarily removing each behavior, running
the named test, and restoring the original file:

```text
without intent recovery: TestWorkerBatchCompletionRestartWindowsNeverReplayPatch FAIL
  after_intent_before_apply and after_apply_before_report got blocked, want uncertain
without resume event replay: TestWorkerIntegrationResumeAfterDurableOutcomeBeforeEvent FAIL
  checkpoint, assistant.ready, assistant.effect_pending: replayed events=[]
without print event subscription: TestCollectorWorkerIntegrationVisibleWithoutModelNarration FAIL
  text result hid integration outcome
without full-response completion callback: TestRunWorkerBatchAutomaticIntegration/disjoint FAIL
  expected one.txt applied; file does not exist
with the reviewed early task-only hook restored: TestRunWorkerBatchAutomaticIntegration/later_parent_write FAIL
  unexpected two.txt applied after the parent's later write
without the next-request decision ID: TestWorkerIntegrationCarriesOutcomeIntoNextRequestState FAIL
  integration ID lost before next request
```

Final gate logs (both exit 0):

```text
GOCACHE=/private/tmp/kiln-patch-integration-go-cache make check
==> gofmt
==> go vet
go vet ./...
==> staticcheck
go run honnef.co/go/tools/cmd/staticcheck@2026.2.2 ./...
==> go test
go test ./...
==> go vet (e2e build tag)
go vet -tags e2e ./test/e2e/...

GOCACHE=/private/tmp/kiln-patch-integration-go-cache make e2e
go test -tags e2e ./test/e2e/... -count=1 -timeout 10m
ok github.com/andrepato/harness/test/e2e 222.029s
```

## Remaining limits

Automatic integration cannot atomically commit a Git working-tree change and
the session record in one transaction. A restarted intent is therefore an
explicit uncertain decision, even if the patch may not have been applied.
Recovered clean pre-apply batches also require explicit handling. This round
did not run an interactive terminal drive; the coordinator will run the final
integrated terminal check.
