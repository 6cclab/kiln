# Independent reviewer 2 — memory import

Reviewed anonymous snapshots in order 04, 03, 02, 01. Scores are 0–4 in rubric order. Findings below are contract violations or compatibility regressions, with probe evidence kept separate from grader infrastructure errors.

## candidate-04

**Scores:** Correctness and robustness: 3, Security and privacy: 2, Design and maintainability: 4, Test quality: 3, Scope and usability: 4. **Readiness:** major revisions.

- **Correctness and robustness:** Core CLI contract, transaction, history, and source tests pass; nonregular FIFO source can hang.
- **Security and privacy:** Source validation opens a destination FIFO before regular-file check, causing unbounded blocking; scope, size, and disclosure checks otherwise pass.
- **Design and maintainability:** A narrow CLI dispatch and shared putTx reuse Put validation and one transaction without new dependencies.
- **Test quality:** Submitted store/CLI tests plus a real-binary e2e cover transfer and sources, but omit FIFO, cancellation, and injected rollback failures.
- **Scope and usability:** Existing memory help is preserved; documentation and diagnostics are useful; no unrelated changes.

Findings, ordered by severity:

- **High — contract violation, reproduced:** `internal/memory/sources.go:30`. Trigger: Import an otherwise valid record whose rebound destination path is a FIFO with a valid-form SHA-256. Observed: Supplemental probe did not return within 3 seconds and terminated the process; RebindSources at line 143 calls sourceDigest, which opens before f.Stat checks regularity. Expected: Reject nonregular source files promptly without blocking.

## candidate-03

**Scores:** Correctness and robustness: 2, Security and privacy: 2, Design and maintainability: 3, Test quality: 3, Scope and usability: 2. **Readiness:** major revisions.

- **Correctness and robustness:** Main acceptance passes, but dry-run attempts writes and a source FIFO hangs.
- **Security and privacy:** Shared sourceDigest open-before-stat order permits an unbounded FIFO hang; other path/scope/privacy checks pass.
- **Design and maintainability:** Validation and transactional writes are cohesive, though dry-run is implemented by rollback after writes and all memory commands bypass the parser.
- **Test quality:** Submitted tests cover history, conflicts, sources and final dry-run state, but miss attempted writes, FIFO, cancellation and injected failures.
- **Scope and usability:** Documentation is broad, but existing kiln memory --help changes from successful global help to exit 2 usage.

Findings, ordered by severity:

- **High — contract violation, reproduced:** `internal/memory/sources.go:30`. Trigger: Rebound destination source is a FIFO. Observed: Supplemental probe timed out after 3 seconds; rebindSources in internal/memory/import.go:214 reaches sourceDigest open before regular-file check. Expected: Reject nonregular source promptly.
- **High — contract violation, reproduced:** `internal/memory/import.go:350`. Trigger: Run --dry-run against a database with an INSERT-rejecting trigger. Observed: Supplemental probe returns SQL constraint error; insertPrepared executes INSERT before DryRun branch at line 359. Expected: Dry run performs validation/conflict analysis and attempts no memory, search or source writes.
- **Medium — compatibility regression, reproduced:** `cmd/kiln/main.go:78`. Trigger: Invoke kiln memory --help. Observed: Supplemental probe gets exit 2 usage; baseline gets exit 0 global help because the new dispatch bypasses cli.Parse for all memory invocations. Expected: Preserve existing memory command help semantics.

## candidate-02

**Scores:** Correctness and robustness: 3, Security and privacy: 2, Design and maintainability: 3, Test quality: 3, Scope and usability: 4. **Readiness:** major revisions.

- **Correctness and robustness:** Core acceptance and no-write dry run pass; FIFO source violates prompt nonregular-file rejection.
- **Security and privacy:** Source validation calls baseline sourceDigest, which opens FIFOs before checking file mode; other boundaries and scope isolation pass.
- **Design and maintainability:** Import owns validation and one transaction with clear branches; it repeats some Put insert logic instead of sharing it.
- **Test quality:** Submitted CLI/store tests cover adversarial JSON, history, sources, conflicts and dry run, but miss FIFO, cancellation and injected rollback.
- **Scope and usability:** Import-only dispatch preserves existing memory help; documentation and success/error output meet the contract.

Findings, ordered by severity:

- **High — contract violation, reproduced:** `internal/memory/sources.go:30`. Trigger: Rebound destination source is a FIFO. Observed: Supplemental probe timed out after 3 seconds; reboundSources in internal/memory/import.go:243 reaches sourceDigest open before file mode check. Expected: Reject nonregular source promptly.

## candidate-01

**Scores:** Correctness and robustness: 2, Security and privacy: 2, Design and maintainability: 3, Test quality: 3, Scope and usability: 4. **Readiness:** major revisions.

- **Correctness and robustness:** Core acceptance passes, but dry-run attempts writes and source FIFO hangs.
- **Security and privacy:** Source validation uses sourceDigest, which opens before checking regularity; other source/path/scope/privacy checks pass.
- **Design and maintainability:** Validation, selection and transaction are cohesive; dry-run relies on rollback after write statements.
- **Test quality:** Submitted tests cover input, source binding, history and final dry-run state, but omit attempted-write, FIFO, cancellation and injected rollback cases.
- **Scope and usability:** Import-only dispatch keeps help behavior; documentation, diagnostics and explicit stdin support are usable and within task scope.

Findings, ordered by severity:

- **High — contract violation, reproduced:** `internal/memory/sources.go:30`. Trigger: Rebound destination source is a FIFO. Observed: Supplemental probe timed out after 3 seconds; RebindSource at line 106 reaches sourceDigest open before file mode check. Expected: Reject nonregular source promptly.
- **High — contract violation, reproduced:** `internal/memory/import.go:205`. Trigger: Run --dry-run against a database with an INSERT-rejecting trigger. Observed: Supplemental probe returns SQL constraint error; INSERT into memories/search/sources occurs before dryRun rollback branch at line 226. Expected: Dry run performs validation/conflict analysis and attempts no memory, search or source writes.

## Comparison and evidence

All four pass the main hidden CLI acceptance suite, formatting, build, vet, targeted race and memory e2e checks. Candidate-04 has the strongest implementation reuse and submitted binary e2e. Candidate-02 is similarly sound on observable behavior but duplicates Put SQL. Candidate-01 and candidate-03 additionally violate strict no-write dry-run behavior; candidate-03 also regresses existing memory help. All share the source FIFO blocker.

gofmt, build, vet, corrected staticcheck, targeted race, memory e2e, and 29/29 hidden CLI acceptance cases passed; full unit gate failed/inconclusive from baseline-sensitive grader environment.

FIFO source timeout all four; exactly 1000 input rows accepted all four; dry-run no-write trigger passed 02/04 and failed 01/03; existing kiln memory --help passed 01/02/04 and failed 03.

**Grader artifacts to exclude from candidate scoring:**

- Initial staticcheck failed only because uncached @2026.2.2 lookup was blocked by GOPROXY=off; corrected offline invocation of the same staticcheck release passed all four.
- Initial full go test ./... failed in unchanged internal/sandbox due an overlong Unix socket path under grader TMPDIR.
- Full-unit rerun with short TMPDIR failed in unchanged internal/tui TestHandleSubmit_LeadingPathIsNotACommand in all four. Independent clean baseline control failed that focused test identically under short TMPDIR and passed under native environment. Thus full unit gate is failed/inconclusive, not passed and not attributed to the patches.

**Uncertainty:** The no-write dry-run finding follows the task's explicit wording and a trigger probe, although rolled-back writes leave the final database unchanged. Source-file validation against concurrent filesystem mutation is outside the practical guarantee of a SQLite transaction; no additional finding assigned.
