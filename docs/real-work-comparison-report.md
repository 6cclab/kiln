# Real-work code-quality comparison — 2026-11-11

Kiln was cheaper on this task, but this experiment does **not** show that it
produces better code than Claude Code. All four submissions passed the 29
predeclared acceptance cases; independent review and follow-up probes found
shipping blockers in every submission. Both blind reviewers rated all four
**major revisions**. The first attempt from each harness was closer to the
contract than its second attempt.

The task was a production-sized feature in Kiln itself: safe memory import with
strict decoding, history selection, project/global isolation, source-file
rebinding, conflict policies, dry-run analysis, atomic SQLite writes, tests and
documentation. The [exact task](../eval/real-work/memory-import/task.md),
[rubric](../eval/real-work/memory-import/rubric.md),
[evaluation tools](../eval/real-work/memory-import/README.md), and
[sanitized results](real-work-comparison/results.json) are retained.
The candidate feature patches are evaluation artifacts, **not merged product
implementations**.

## Observed results

| Harness / attempt | Anonymous ID | Process result | Full wall time | Reported cost | Acceptance | Both reviewers |
|---|---|---|---:|---:|---:|---|
| Claude Code 1 | 04 | Completed | 12m 42s | $4.9191 | 29/29 | Major revisions |
| Kiln 1 | 02 | Completed | 9m 22s | $3.0634 | 29/29 | Major revisions |
| Kiln 2 | 01 | Completed | 8m 29s | $2.6345 | 29/29 | Major revisions |
| Claude Code 2 | 03 | Budget exhausted, exit 1 | 18m 38s | $6.0082 | 29/29 | Major revisions |

The real-work attempts reported **$16.625258** combined. Including the earlier
$1.1484105 small-fixture cohort, total reported spend is **$17.7736685 of the
$50 authorized allowance**. These are reported/catalog estimates, not an
invoice. Reviewer-agent usage is separate and is not available as provider
billing in this external Opus comparison ledger.

Claude Code attempt 2 emitted intermediate success results at cumulative
$4.3407525 and $5.348418, continued handling background test activity, then
emitted `error_max_budget_usd` at cumulative $6.008204. Those totals belong to
one session and must not be added together. The runner stopped all paid work
on process exit 1. The table uses the full process duration and final patch;
there was no snapshot at the first success event and no replacement attempt.
Its $6 soft cap was slightly exceeded by the final request. The different
budget admission mechanisms limit interpretation of its incomplete outcome.

## Defects that passing tests missed

| Reproduced behavior | Kiln 1 | Kiln 2 | Claude 1 | Claude 2 |
|---|---|---|---|---|
| Destination source is a FIFO: import blocks instead of rejecting it | Fails | Fails | Fails | Fails |
| Dry-run attempts no memory INSERTs | Passes | Fails | Passes | Fails |
| Existing `kiln memory --help` succeeds | Passes | Passes | Passes | Fails |
| Exactly 1000 valid records can be imported | Passes | Passes | Passes | Passes |

**Source-file hang, all four:** each new import path calls the existing
`sourceDigest` helper, which opens the destination path before checking that it
is regular. A named pipe with no writer blocks in `os.Open`; the bounded probe
terminated each CLI after three seconds. The requirement explicitly rejects
nonregular sources. This is a defect in how the new feature relies on an
existing helper, rather than a new bug in the helper itself. The probe shows
blocking behavior, not data loss or remote code execution.

**Dry-run writes, Kiln 2 and Claude 2:** both perform SQL updates/inserts and
then roll back. A trigger that rejects INSERTs causes their dry-run to fail.
The task explicitly requires validation and conflict analysis without writing
memory/history/search/source entries; preserving only the final persisted
state is insufficient for that wording. The other two candidates branch before
writes and pass the same probe. No persistent database corruption was observed.

**Help regression, Claude 2:** its new dispatch sends every `memory` command
around the shared argument parser. `kiln memory --help` returns exit 2; the
frozen baseline and other submissions return exit 0. Only import needed special
flag handling.

These four probes were added **after submission review**, applied identically
to every candidate, and reported separately from the 29 predeclared cases.
The [probe source](../eval/real-work/memory-import/supplemental_probes.py) and
per-candidate outputs are retained. The submitted patches were never repaired
or given evaluator feedback.

## Independent code review

Two GPT-6-Sol reviewers saw anonymous full snapshots, the same task/rubric, and
objective evidence in opposite orders. They did not receive harness identities,
prices, timings, model transcripts, or each other's assessments. Earlier
patch-only reviews of the first three completed artifacts were also anonymous.
The root reviewer checked the findings against the contract and reproduced
counterexamples uniformly. These are agent-assisted reviews, not human ground
truth.

Scores are 0–4 in this order: **correctness, security, design, submitted tests,
scope/usability**. They are kept separate rather than averaged into a score
that could hide a blocker.

| Submission | Reviewer 1 | Reviewer 2 |
|---|---|---|
| Kiln 1 / 02 | 3, 2, 3, 3, 3 | 3, 2, 3, 3, 4 |
| Kiln 2 / 01 | 2, 2, 3, 3, 3 | 2, 2, 3, 3, 4 |
| Claude 1 / 04 | 3, 2, 3, 3, 3 | 3, 2, 4, 3, 4 |
| Claude 2 / 03 | 2, 2, 2, 3, 2 | 2, 2, 3, 3, 2 |

Both reviewers found substantial useful implementation and test work. Claude 1
reuses a transactional insertion helper with existing Put and adds a
real-binary e2e test. Kiln 1 keeps validation and import in a cohesive store path,
but duplicates some insertion logic. Reviewer 2 preferred Claude 1's reuse;
reviewer 1 rated their designs equally. Both identified missing submitted
regressions for FIFO blocking and injected write failures/cancellation. The
independent evaluator supplies some of that evidence; it does not improve the
quality of the tests the harness actually submitted.

Full independent assessments, source-line references and uncertainty are in
[reviewer 1](real-work-comparison/reviewer-one.md) and
[reviewer 2](real-work-comparison/reviewer-two.md). Root adjudication agrees with
the three reproduced defects. Ordinary conflict-key diagnostics and filesystem
changes after validation were considered but not promoted into unsupported
contract violations.

## Verification and experimental limits

All four patches applied and passed formatting, compilation, vet, corrected
offline staticcheck, focused memory/CLI race tests, memory e2e tests, and all 29
hidden CLI cases. The grader checks patch hashes and source status before and
after testing; submitted sources remained unchanged.

The first grading pass had two infrastructure failures: offline staticcheck
release-name resolution, and a Unix socket path too long under the grader's
temporary directory. The cached canonical staticcheck module was verified as
version 2026.2.2 and passed for every candidate. Shortening the temporary path
resolved the socket failure but exposed an unchanged TUI test failure in the
full unit suite. Original failures and reruns are retained; full unit results
must not be represented as an all-green candidate suite. A clean baseline clone
reproduced that focused TUI failure under the short temporary path and passed
under the native environment. Path-dependent wrapping is the likely mechanism,
inferred from the rendering code. The final full-unit status is
**failed/inconclusive in the grading environment**, not a candidate pass.

The hidden cancellation test terminates an import while waiting for a database
lock; it does not prove cancellation after an in-flight row write. The suite
injects memory/source-table failures and snapshots FTS contents, but does not
inject a fault inside the FTS implementation. See the
[test method](../eval/real-work/memory-import/test-method.md).

The baseline was `40e946486120497606d0e0070180dfb9aad24f05`. Both arms used
Opus 4.8 at high effort, fresh independent repository clones and scratch personal
configuration, WebSearch disabled, an 80-turn setting, and a 20-minute process
limit. The frozen order was Claude 1, Kiln 1, Kiln 2, Claude 2. Executables,
prompt, rubric and hidden suite were fingerprinted before inference. No
unrelated builds ran during timed inference. Shared Go caches were warmed but
not filesystem-enforced read-only.

Native tool behavior and budgeting were preserved: Claude Code 2.1.319 had a
$6 soft per-run cap; Kiln used request admission with a conservative full-context
reservation and a larger ceiling. Claude reported one-hour cache writes;
Kiln's isolated configuration used its default five-minute cache policy. Thus
reported cost differences include native cache policy. Claude also reported a
system-wide managed-settings warning despite its scratch personal config.
This was not a fully hermetic or pure same-settings experiment.

No paid worker delegation was observed in these attempts. Faux model routes in
Kiln's combined logs came from tests and are not paid helper-model evidence.
Personal memory and learning were disabled. This experiment therefore does not
establish cross-model orchestration efficiency, memory quality, or improvement
across sessions. One feature and two attempts per harness cannot establish
statistical or general superiority.

## Improvements supported by this evidence

1. Add independent contract-focused review before calling a coding task complete.
   Exercise inherited helpers at adversarial boundaries, especially blocking I/O
   and promises that an operation performs no writes.
2. Keep readiness blockers separate from acceptance percentages and cost. Add
   these counterexamples to future regression suites while preserving this
   frozen experiment's original tests.
3. Track background work through process termination. An intermediate success
   event is not sufficient for completion or final cost accounting.
4. Test orchestration and learning in separate cohorts, with matched budget/cache
   policies and explicit cold-versus-learned state. The present result does not
   measure those capabilities.

The pending context-screen CI fix is independent: it normalizes path-dependent
percentages in two golden rows and adds a regression test. On the implementation
branch, Go 1.26.6 `make check` and full `make e2e` passed (223.043 seconds for
e2e). No candidate import implementation was merged as part of that fix.
