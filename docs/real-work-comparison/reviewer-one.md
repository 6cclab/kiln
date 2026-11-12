# Independent review: memory import

I reviewed the four anonymous frozen snapshots in candidate order against task.md and rubric.md. Scores use the rubric's 0–4 anchors. The shared source FIFO issue is an explicit contract blocker, so no patch is ready despite all four passing the 29 named hidden acceptance cases. These are candidate findings, not deductions about authorship.

| Candidate | Correctness | Security | Design | Tests | Scope and usability | Readiness |
|---|---:|---:|---:|---:|---:|---|
| 01 | 2 | 2 | 3 | 3 | 3 | major revisions |
| 02 | 3 | 2 | 3 | 3 | 3 | major revisions |
| 03 | 2 | 2 | 2 | 3 | 2 | major revisions |
| 04 | 3 | 2 | 3 | 3 | 3 | major revisions |

## Candidate 01

1. **Contract/security blocker, reproduced.** [`internal/memory/sources.go:30`](candidate-01.patch) opens the destination source before `f.Stat()` checks regularity at line 39. The new `RebindSource` calls this helper at line 106. A named pipe at the destination source path with no writer leaves `kiln memory import` blocked rather than rejecting a nonregular source. The supplemental `fifo_source_rejected_without_blocking` probe timed out after 3 seconds and terminated the process. This is a feature-specific use of a baseline helper; it is still a contract failure for import.
2. **Contract blocker, reproduced.** [`internal/memory/import.go:202`](candidate-01.patch) updates/inserts memories, search, and sources before the dry-run branch at line 226 rolls the transaction back. The dry-run contract calls for validation and conflict analysis without these writes. An INSERT-blocking trigger made a dry run exit 1 (`constraint failed: review probe observed INSERT`), while dry-run counts should have succeeded. Final persisted state remains unchanged, but write attempts make behavior observably different.

History collapse, scope isolation, global historical validation, ordinary source rebinding, conflicts, rollback on ordinary write errors, and 1000-row boundary passed objective checks. `Import` owns a single transaction and preserves local correction lineage. Submitted unit tests cover JSON shape, limits, policies, sources, and dry-run persisted state; they omit nonregular FIFO and write-trigger dry run. Documentation is detailed and compatible with the command. No dependencies or unrelated changes were found. Score reasons: correctness 2 for dry-run semantics; security 2 for the unbounded FIFO wait; design 3 for cohesive import and explicit transaction; tests 3 for broad behavior tests with the two adversarial gaps; scope/usability 3 for compatible commands and useful docs.

## Candidate 02

1. **Contract/security blocker, reproduced.** [`internal/memory/import.go:243`](candidate-02.patch) calls `sourceDigest`, which opens the destination source in [`internal/memory/sources.go:30`](candidate-02.patch) before its regular-file check at line 39. The same named-pipe probe timed out after 3 seconds. Nonregular source files must be rejected.

The store validates every imported historical row before selection, including global rules and source freshness, and uses one transaction. Its dry run branches before SQL writes at [`internal/memory/import.go:127`](candidate-02.patch); the write-trigger probe passes. Source and scope boundaries, conflicts, cancellation while waiting on the database, and rollback pass the 29 named hidden cases. Submitted tests exercise selection, validation, sources, conflict policies, and dry-run final state, but do not cover FIFO or database write failure/cancellation directly. The CLI tests use the command handler; no submitted real-binary e2e test was added. Docs cover the user path and limitations. Score reasons: correctness 3 and design 3 for sound transactional behavior with the shared source flaw; security 2 for the hang; tests 3 for substantial behavioral coverage with adversarial gaps; scope/usability 3 for compatibility and clear docs.

## Candidate 03

1. **Contract/security blocker, reproduced.** [`internal/memory/import.go:214`](candidate-03.patch) calls baseline `sourceDigest`, whose [`os.Open` at `internal/memory/sources.go:30`](candidate-03.patch) precedes regularity validation. The named-pipe destination source probe timed out after 3 seconds.
2. **Contract blocker, reproduced.** [`internal/memory/import.go:350`](candidate-03.patch) inserts in both real and dry runs, then rolls back at line 359. The insert-trigger dry-run probe exits 1 rather than reporting would-be counts. Final persisted state rolls back, but the dry run attempts forbidden writes.
3. **Compatibility regression, reproduced.** [`cmd/kiln/main.go:78`](candidate-03.patch) routes every `memory` invocation ahead of the shared parser. `kiln memory --help` exits 2 with a memory usage error; the frozen baseline exits 0 with global help. The supplemental `existing_memory_help_preserved` probe confirms this. Only import needed special flag parsing.

The import validates history and sources before a single transaction and preserves local lineage. The submitted tests cover history selection, rollback on conflict, restore after replace, source path failures, and limits; they do not expose the trigger/FIFO issues and add no real-binary e2e drive. Docs are comprehensive. Score reasons: correctness 2 for dry-run behavior, security 2 for FIFO, design 2 for broad CLI routing and write-then-rollback dry run, tests 3 for broad behavior coverage, scope/usability 2 for the existing help regression.

## Candidate 04

1. **Contract/security blocker, reproduced.** [`internal/memory/sources.go:143`](candidate-04.patch) calls `sourceDigest`, which opens the destination source at line 30 before its regular-file check at line 39. The named-pipe destination source probe timed out after 3 seconds. Import must reject nonregular sources.

The CLI validates all history and rebinds sources before invoking a single transactional `Store.Import`. Its dry run skips writes at [`internal/memory/import.go:96`](candidate-04.patch), and the write-trigger probe passes. Reusing `putTx` keeps normal Put and import on one insertion path; changed Put behavior remains covered by existing tests. The submitted tests include unit validation/policies and a real-binary e2e import/export/source-rebinding drive. They omit FIFO and an explicit failed-write rollback test. Docs are detailed and existing memory commands remain routed through the original parser. Score reasons: correctness 3 and design 3 for the focused implementation with one source flaw; security 2 for FIFO; tests 3 for meaningful unit plus e2e coverage with adversarial gaps; scope/usability 3 for compatible command flow and docs.

## Comparison and uncertainty

02 and 04 are the closest to the contract: dry-run writes are avoided and existing memory command parsing is preserved. 04 has stronger submitted real-binary integration evidence; 02 keeps import write logic in one store function without changing Put. 01 and 03 require an additional dry-run correction; 03 also needs its memory command routing narrowed. All four require a source regularity check before blocking `open` (and a regression test using a FIFO with a deadline). This is a narrow repair in code, but the current infinite wait on an imported nonregular source prevents a ready rating.

The 29 named hidden cases pass for all four, including cancellation while waiting for the database, rollback on memory/source write failures, source boundaries and stale history. The supplemental probes expose behavior outside those named cases. I did not score ordinary conflict messages that include a key as a credential leak: the contract does not clearly require suppressing every ordinary key, and the hidden diagnostic test passes. Optional improvements would include a direct cancellation/write-failure regression test in submitted suites. The initial staticcheck failure was an offline module-version lookup issue; the corrected offline rerun of the same staticcheck release passes all four. The initial full unit run failed on an overlong grader temporary Unix socket path. Under a shorter temporary path, the unchanged TUI `TestHandleSubmit_LeadingPathIsNotACommand` times out in all four; an independent clean baseline clone fails the focused test identically there and passes in its native environment. The full unit gate therefore remains failed/inconclusive under the grading environment, **not passed**. These failures are not attributed to the import patches (see each `grading/infrastructure-classification.json`).
