# Worker snapshots and declared verification dependencies

This roadmap tranche adds opt-in dirty-parent isolation and directory-aware
verification evidence. It does not establish output-quality or cost parity with
Claude Code or Codex; paid comparisons remain deferred.

## Dirty-parent workers

Enable `orchestration.isolatedWorkers` and `orchestration.snapshotDirty` to let
file-edit workers start from staged, unstaged and nonignored untracked inputs.
The parent checkout and index remain intact. Worker artifacts contain only the
worker delta and bind to HEAD plus a captured content/index baseline. Single or
batch application refuses parent drift and preserves the parent's index.

Capture is bounded to 1,000 changed/untracked paths and 4 MiB. Ignored files are
excluded. Git filters/hooks are suppressed. Skip-worktree, assume-unchanged and
unmerged index states are refused rather than treated as complete snapshots.
Shell/sandbox support and automatic patch integration remain outstanding.
See [configuration](configuration.md#isolated-editing-workers) for usage and
integration limitations.

A local microbenchmark caught repeated Git subprocess overhead before shipping.
Batching object hashing and index updates reduced the observed setup time:

| Changed files | Initial implementation | Batched implementation |
|---|---:|---:|
| 1 | 169 ms | 214 ms |
| 100 | 2,179 ms | 217 ms |
| 1,000 | 25,750 ms | 401 ms |

These are single samples on an Apple M4 Pro, with other validation running.
They measure snapshot construction only, excluding worktree checkout, model
calls and patch export/application. They are a local implementation comparison,
not a harness benchmark. Reproduce with:

```sh
GOTOOLCHAIN=go1.26.6 go test ./internal/agent -run '^$' \
  -bench '^BenchmarkWorkerSnapshot$' -benchtime=1x -count=1
```

## Verification evidence and plans

Bash can now declare recursive `verificationDirectories` alongside
`verificationFiles`. Successful unchanged before/after snapshots retain bounded
file content, permissions and membership evidence. New/deleted descendants make
the proof stale; missing, denied, special or over-limit inputs cannot produce a
partial proof. Directory reads are bounded before enumeration is allocated.

`verification_status` with `mode:"plan"` suggests exact prior commands when
inputs changed. Unavailable evidence requires review and redacts command details.
Unchanged evidence is omitted without claiming that skipping checks is safe.
Evidence remains branch-aware and survives checkpoint restoration. No automatic
test dependency discovery or command execution is introduced.

## Validation

The new real-CLI scenarios fail against `2e26fe0`: dirty worker dispatch is
refused and directory-based command evidence is absent. They pass with this
implementation. They exercise parent index preservation, stale snapshot refusal,
worker-only patch application, and rerun planning after adding a source file.

Unit regressions cover batch integration, manifest drift, task-result baseline
propagation, nested worker forwarding, ignored files, renames, line-ending
normalization, unsafe filesystem inputs, limits, permissions, cancellation, legacy
proofs and branch restoration. Existing file-only evidence and clean-checkout
worker behavior remain covered.

The `/context` fixture's tool count intentionally changes from 3.6k to 3.8k
because the verification tool schema grew; only that value changes in its two
goldens. In a full run, the agents and permission style fixtures each captured a
partial frame; three focused reruns of each passed without changing their
expectations.
The final full end-to-end suite passed in 223.676 seconds. The real-terminal
`commands/context.steps` scenario passed, and its screenshot was inspected.
