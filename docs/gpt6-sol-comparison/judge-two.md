# Independent patch review

Reviewed candidate-02, then candidate-01, against `task.md` and the unchanged five-dimension rubric. It uses only the anonymous bundles and completed grading evidence. Scores are 0–4.

| Candidate | Correctness | Security and privacy | Design and maintainability | Test quality | Scope and usability | Readiness |
|---|---:|---:|---:|---:|---:|---|
| 02 | 2 | 2 | 3 | 2 | 4 | Major revisions |
| 01 | 2 | 3 | 3 | 2 | 3 | Minor revisions |

## Candidate 02

**Correctness and robustness — 2.** The implementation validates the full input history, collapses by maximum ID, checks conflicts inside the write transaction, and writes memory, search, and bindings in that transaction (`internal/memory/import.go:62–175`). The nonregular-source case below can hang indefinitely, including during a dry run. The 29 named hidden acceptance cases passed; that suite did not exercise this FIFO condition.

**Security and privacy — 2.** Scope is selected by the command, imported scope is not used for writes (`internal/cli/personal_memory.go:88–96`, `internal/memory/import.go:77–80`); file size and path checks exist. A crafted source path can block the local management process, contrary to rejection of nonregular source files. Diagnostic paths do not echo record text or credentials in the tested cases.

**Design and maintainability — 3.** A cohesive `Store.Import` API owns validation and transaction boundaries. Reusing `validatePutRecord` avoids field-rule drift. The source rebind path reuses `sourceDigest`, but that helper opens a path before it verifies file type. The argument parser special case in `internal/cli/args.go:197–200` is narrow and works for the specified syntax, though it couples import flags to the general parser.

**Test quality — 2.** Submitted tests cover history collapse, dry run, all conflict policies, scope isolation, source rebinding, historical invalidity, rollback triggers, and cancellation. They omit a FIFO source. `internal/memory/import_test.go:198` compares a canonicalized stored root to a possibly noncanonical `t.TempDir()` spelling. On this macOS runner the normal targeted unit and race runs fail because `/tmp` resolves to `/private/tmp`; both pass without source changes when `TMPDIR` uses the canonical spelling. This is a test-portability defect, not demonstrated runtime misbinding. The full unit run also has an unchanged TUI test failure reproduced on baseline. Existing memory e2e tests, build, formatting, vet, and staticcheck pass.

**Scope and usability — 4.** The CLI accepts flags after the filename, rejects bad arguments without success JSON, and gives concise diagnostics (`internal/cli/personal_memory.go:149–202`). Documentation explains history, conflicts, sources, and dry-run behavior (`docs/configuration.md:836–881`). No new dependency or unrelated source refactor appears in the patch.

### Findings

1. **Contract violation, major: nonregular source can hang import.** `internal/memory/import.go:202–204` calls `sourceDigest`; `internal/memory/sources.go:30–39` opens the resolved path before checking `Mode().IsRegular()`. Put a FIFO named `pipe` in the destination project, then import a valid-shape record whose source is `{root:"/old/project",path:"pipe",sha256:"000…000"}`. The candidate-02 built CLI did not return within 2 seconds in my isolated probe; the completed supplemental probe also timed out after 3 seconds. The contract requires rejection of nonregular source files, including historical rows, so import should return a failure without waiting for a FIFO writer. This also prevents cancellation from taking effect while `os.Open` blocks. Reproduced.
2. **Test-portability defect, minor:** `internal/memory/import_test.go:198` uses string equality for a root that import canonicalizes. With `TMPDIR` under `/tmp` on this machine, expected `/tmp/...` differs from actual `/private/tmp/...`; the canonical-temp rerun passes. This should be fixed in the submitted regression test. Reproduced in grading logs.

Transaction and cancellation: explicit `ctx.Err()` checks occur during record validation and before commit (`internal/memory/import.go:65, 148, 170`), and `BeginTx(ctx)` ties database work to cancellation. Failure paths defer rollback. The FIFO opening is outside the transaction and does not observe cancellation. For normal regular files, source validation, conflict analysis, and all writes are appropriately ordered; dry runs do not write records.

## Candidate 01

**Correctness and robustness — 2.** Batch validation and transactional conflict handling are sound in the tested cases (`internal/memory/import.go:49–161`), including replacement lineage and rollback. The imported-scope check below rejects valid project imports under the task's explicit rule that input scope is not destination identity. The hidden suite passes 27 of 29 named cases; `project_scope_isolation` and `replace_lineage` fail for this reason.

**Security and privacy — 3.** Source rebinding checks lexical containment, resolved destination containment, regularity, and size before opening (`internal/memory/import.go:166–206`), and the FIFO probe returns promptly. Scope isolation is preserved in writes and diagnostics avoid imported text. The extra input-scope validation restricts legitimate imports rather than leaking data.

**Design and maintainability — 3.** Import-specific CLI parsing is separated from the chat flag parser (`internal/cli/memory_import.go:17–118`) and the store keeps a single transaction. Field and kind validation is duplicated from `Put` instead of reusing its limits, which creates a maintenance risk. The pre-open file-type guard is a useful defense.

**Test quality — 2.** Submitted tests exercise invalid JSON and history, conflict policies, source boundaries, nonregular and oversized files, global history, rollback triggers, and cancellation. They do not cover project import of an input row with an arbitrary or contradictory scope string. Two new tests compare `/tmp` paths with canonicalized `/private/tmp` paths (`internal/memory/import_test.go:149–150`, `internal/cli/memory_import_test.go:87–89`). Normal unit and targeted race runs fail on this runner; both targeted suites pass with canonical `TMPDIR`. The unrelated TUI test failure in full unit testing was reproduced on baseline. Build, formatting, vet, staticcheck, and existing memory e2e tests pass.

**Scope and usability — 3.** The command handles specified flags and JSON output, and documentation covers transfer, conflicts, and dry runs (`docs/configuration.md:836–875`). The direct dispatch in `cmd/kiln/main.go:74–76` is a small extra integration point; existing memory operations remain intact in acceptance checks. Diagnostics are useful without disclosing imported content.

### Findings

1. **Contract violation, moderate: input scope can reject a project import.** `internal/memory/import.go:67–69` rejects any input record whose `scope` string is `user:preferences` unless it is a source-free preference, even when the destination is a project. The task says not to trust imported scope as destination identity and to apply the destination's project/global rule; all input records must still meet field, kind, source, and expiry limits. Give a valid fact record `scope:"user:preferences"`, then run `kiln memory import file.json` from a project. The CLI returns `memory import has invalid global record` and writes nothing; it should import the fact into the current project. Reproduced by the completed `project_scope_isolation` and `replace_lineage` hidden cases. The error is an over-restriction, not a privacy leak.
2. **Test-portability defect, minor:** `internal/memory/import_test.go:149–150` and `internal/cli/memory_import_test.go:87–89` expect literal temp path spelling. Import canonicalizes the project path, so `/tmp/...` and `/private/tmp/...` differ as strings on this runner. Both failures disappear with canonical `TMPDIR`; the production behavior correctly binds to the canonical root. Reproduced in grading logs.

Transaction and cancellation: record validation precedes `BeginTx`, conflict checks and writes share the transaction, and a deferred rollback covers errors (`internal/memory/import.go:49–161`). Cancellation is checked during validation, source checks, and before commit. Dry runs perform validation and conflict analysis without memory writes. The source file check is not perfectly race-free against concurrent path replacement, but that is shared with the existing source digest model and not a demonstrated contract failure here.

## Comparison and uncertainty

Candidate 02 follows destination scope semantics and passes all 29 named hidden acceptance cases, but its reproducible FIFO hang is a source-validation and cancellation blocker. Candidate 01 rejects that FIFO promptly, yet rejects project imports based on an untrusted input scope; two hidden cases demonstrate the mismatch. Candidate 01's needed behavior correction is localized, so I classify it as **minor revisions**; candidate 02 needs the source opening sequence changed and regression coverage, so I classify it as **major revisions**. Neither is ready as submitted. Both submitted suites have the same temp-path portability defect. A maintainer should decide whether accepting contradictory imported `user:preferences` rows as project facts is indeed the intended reading of “do not trust imported scope”; the hidden acceptance cases say yes, while the ordinary exporter would never emit such a row. No broader quality or harness conclusion follows from these two patches.
