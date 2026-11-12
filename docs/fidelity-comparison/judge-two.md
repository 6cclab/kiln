# Independent patch review (judge two; final)

Review order: 04, 03, 02, 01. I read only the supplied task, rubric, review instructions, candidate source/patches, and each candidate's grading directory. I did not inspect identity or provenance. All probes used fresh `/tmp` projects and homes.

| Candidate | Correctness | Security | Design | Tests | Scope and usability | Readiness |
|---|---:|---:|---:|---:|---:|---|
| 04 | 3 | 3 | 4 | 3 | 4 | major revisions |
| 03 | 2 | 2 | 3 | 3 | 3 | major revisions |
| 02 | 2 | 2 | 3 | 2 | 2 | major revisions |
| 01 | 3 | 3 | 4 | 3 | 4 | major revisions |

## Candidate 04

**Scores.** Correctness 3: validates all records, selects largest IDs, plans conflicts before write, and guards dry-run writes (`internal/memory/import.go:160-289`). Security 3: bounded read (`internal/cli/personal_memory.go:242-265`), strict JSON (`internal/memory/import.go:45-73`), destination-only source rebinding and digest (`internal/memory/import.go:111-151`). Design 4: import owns validation and one transaction; CLI handles parsing and bounded file I/O. Tests 3: meaningful history/source/conflict/dry-run cases (`internal/memory/import_test.go:77-378`), but no cancellation or mid-transaction write-failure rollback test, and historical stale source is not directly asserted. Scope/usability 4: focused files and substantial user docs (`docs/configuration.md:840-875`); current commands stay on existing path (`cmd/kiln/main.go:75-85`).

**Finding 1 — contract violation, reproduced in supplemental CLI probe.** A source path pointing to a FIFO causes the process to hang rather than reject a nonregular source. `internal/memory/import.go:139` calls `sourceDigest`; `internal/memory/sources.go:21-39` opens the path before checking `f.Stat().Mode()`. Trigger: valid project import JSON binding an existing FIFO under the destination root with a syntactically valid SHA-256. Observed: timeout after 3 seconds; expected prompt nonzero rejection. The contract explicitly requires rejection of nonregular source files. This is inherited from the shared digest helper but applies to this new import path.

Optional test improvement: `TestImportAtomicOnFailure` (`internal/memory/import_test.go:358`) feeds a validation error before the transaction, so it cannot establish rollback after an earlier successful SQL write. A fault-injected second INSERT and canceled context would test the harder guarantee. The SQLite transaction uses context-aware calls and deferred rollback (`internal/memory/import.go:211-289`), so the guarantee is plausible from code. Source containment relies on existing `sourceDigest`, which resolves symlinks and rejects oversized files and ordinary nonregular files after opening them (`internal/memory/sources.go:21-51`). Scope is destination-derived in CLI (`internal/cli/personal_memory.go:224-230`) and never imported from JSON.

**Probe.** `[]]` and `[]}` both exit nonzero without JSON. An abort-on-INSERT SQLite trigger does not affect `--dry-run`, which succeeds and reports `inserted:1`.

## Candidate 03

**Scores.** Correctness 2: full history validation and selection (`internal/cli/memory_import.go:209-271`), one transaction, but dry-run performs writes before rollback (`internal/memory/import.go:101-129`). Security 2: source rebinding checks canonical destination and digest (`internal/memory/sources.go:85-127`), but conflict diagnostics include the memory key (`internal/memory/import.go:90`), which could itself contain credentials. Design 3: sound separation, though the store accepts pre-rebound records and depends on CLI validation; dry-run control is placed after writes. Tests 3: CLI and store tests cover malformed JSON, history, sources, conflict policies and persistent dry-run state; they do not catch transient writes or inject a mid-batch SQL failure. Scope/usability 3: docs explain behavior (`docs/configuration.md:840-870`) but claim dry-run writes nothing.

**Finding 1 — contract violation, reproduced.** `internal/memory/import.go:101-125`: `ImportRecords` executes UPDATE/INSERT/search/source SQL and only checks `dryRun` at line 125. Trigger: create a `BEFORE INSERT ON memories` trigger that aborts, then import one valid new record with `--dry-run`. Observed: exit 1, no success JSON, trigger error. Expected: validation/conflict analysis and success JSON with `inserted:1`, because dry runs must write no memory, search, history or source rows. Deferred rollback protects persistent state, but the command still performs writes and can fail for a write-only reason. Fix needs a pre-write dry-run branch, not merely a rollback.

**Finding 2 — contract violation, reproduced in supplemental CLI probe.** `internal/memory/sources.go:21-39` opens a destination FIFO before checking its type, through `RebindSource` (`internal/memory/sources.go:85-127`). Valid import JSON binding that FIFO hangs instead of rejecting the nonregular source. Same trigger and consequence as candidate 04.

**Finding 3 — privacy risk, inferred.** `internal/memory/import.go:90`: conflict error formats `%q` with `r.Key`. Trigger: import a key containing a credential where that key already exists. Expected: useful conflict diagnostic without echoing credentials. This is smaller than finding 1 because ordinary keys are not memory text, but the contract explicitly forbids credential disclosure.

Cancellation uses `BeginTx(ctx)` and context SQL methods; rollback is deferred (`internal/memory/import.go:76-129`). Sources are validated even for unselected historical rows (`internal/cli/memory_import.go:213-252`). Global scope rejects source-bearing and non-preference history (`internal/cli/memory_import.go:226-231`).

## Candidate 02

**Scores.** Correctness 2: history/source/conflict transaction logic largely works, but malformed trailing delimiters are accepted (`internal/cli/personal_memory.go:217`) and dry-run executes writes (`internal/memory/store.go:460-495`). Security 2: source boundaries and scope are handled (`internal/memory/sources.go:93-156`; `internal/cli/personal_memory.go:214-266`), but conflict diagnostics echo keys (`internal/memory/store.go:454`). Design 3: shared Put/import validation is reasonable, but import and CLI parse both have misplaced completion checks. Tests 2: submitted malformed test includes `[] []` (`internal/cli/personal_memory_import_test.go:139`) but omits `[]]`/`[]}`; dry-run tests inspect only final persisted state and miss write attempts. Scope/usability 2: docs (`docs/configuration.md:849-870`) are useful, but the broad command dispatch changes `kiln memory --help` from global help exit 0 to usage exit 2 (`cmd/kiln/main.go:78-79`), and documented strict JSON and no-write dry run are contradicted by probes.

**Finding 1 — contract violation, reproduced.** `internal/cli/personal_memory.go:217`: `dec.More()` after decoding the top-level array returns false for stray closing delimiters. Trigger: file containing `[]]` or `[]}`. Observed: exit 0 and success JSON with all counts zero. Expected: nonzero, diagnostic, no success JSON because input is malformed/trailing JSON. A second Decode expecting EOF would enforce the contract. The submitted test's `[] []` is rejected, so its coverage gives false confidence for this case.

**Finding 2 — contract violation, reproduced.** `internal/memory/store.go:460-492`: dry-run performs UPDATE/INSERT/search/source SQL and then rolls back. With an abort-on-INSERT trigger and a valid new record, `--dry-run` exits 1; expected success counts and no write attempt. Persistent database rows are rolled back, but the contract requires dry-run to write none.

**Finding 3 — contract violation, reproduced in supplemental CLI probe.** `internal/memory/sources.go:21-39` opens a destination FIFO before checking its type, through `RebindSources` (`internal/memory/sources.go:93-130`). Valid import JSON binding the FIFO hangs rather than rejecting a nonregular source.

**Finding 4 — compatibility regression, reproduced.** `cmd/kiln/main.go:78-79` dispatches every `memory` invocation before the shared parser. `kiln memory --help` now exits 2 with usage on stderr; the existing command previously returned help with exit 0. The task requires preserving existing memory commands.

**Finding 5 — privacy risk, inferred.** `internal/memory/store.go:454`: conflict diagnostic includes `r.Key`. A key containing a credential is printed to stderr. The same concern applies to candidate 03.

The selected batch is in one context transaction (`internal/memory/store.go:435-496`); rollback should handle validation, cancellation and SQL failure. Source rebinding validates every historical record before selection (`internal/cli/personal_memory.go:231-263`), including global restrictions. CLI starts no model. Direct source tests include symlink escape, a strength (`internal/memory/import_test.go:180-192`).

## Candidate 01

**Scores.** Correctness 3: validates all records and source-bearing history before selecting max IDs, plans conflicts, and guards dry-run writes (`internal/memory/import.go:58-190`). Security 3: bounded strict decoding (`internal/cli/memory_import.go:168-197`), lexical original-root checks, destination rehash and symlink containment (`internal/memory/import.go:250-306`). Design 4: ownership is clear and the transaction applies only selected records. Tests 3: realistic source-history and conflict checks (`internal/memory/import_test.go:42-332`), but no cancellation or mid-transaction SQL failure test. Scope/usability 4: focused changes and complete docs (`docs/configuration.md:845-872`); existing memory commands remain available.

**Finding 1 — contract violation, reproduced in supplemental CLI probe.** `internal/memory/import.go:276` calls `sourceDigest` after rebinding, and `internal/memory/sources.go:21-39` opens the destination file before checking whether it is regular. A source path pointing to a FIFO blocks the import instead of being rejected. Trigger: valid project import JSON with a syntactically valid SHA-256 and a destination FIFO under the project. Expected: prompt nonzero rejection of a nonregular source. This is inherited helper behavior but still violates the new import contract.

Optional test improvement: `TestImportValidationRejectsAndLeavesDBUnchanged` (`internal/memory/import_test.go:183`) checks pre-write validation, not rollback after a later SQL failure. Inferred privacy edge: CLI usage errors can echo an arbitrary extra argument or invalid policy (`internal/cli/memory_import.go:63,98`); a secret mistakenly passed there would appear in stderr. The core record validation and conflict errors avoid echoing memory content. The transaction uses context-aware SQL, defers rollback and commits once (`internal/memory/import.go:141-190`).

**Probe.** `[]]` and `[]}` exit nonzero without JSON. An abort-on-INSERT trigger does not affect `--dry-run`.

## Comparison and uncertainty

Candidates 01 and 04 are strongest and effectively tied, but both need the shared FIFO rejection fix before readiness. Candidate 04 has broader submitted validation tests, while candidate 01 directly tests stale historical sources; both lack injected rollback/cancellation cases. Candidate 03 additionally needs a true read-only dry-run. Candidate 02 additionally needs strict trailing JSON detection and preservation of `memory --help`. These are explicit contract issues, so their readiness cannot be raised by averaging the other dimensions.

All four candidates pass build, gofmt, vet, staticcheck, changed-package race tests, memory e2e, and 29/29 hidden acceptance cases. All four full unit runs fail the same `TestHandleSubmit_LeadingPathIsNotACommand` timeout; the unmodified baseline fails identically under the grader environment, so I do not attribute it to these patches. Supplemental probes independently establish the FIFO, dry-run, help, and malformed-delimiter findings. No candidate source was modified.
