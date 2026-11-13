# Post-fix real-work comparison

Kiln completed both attempts with **32.5% lower reported cost and 42.7% less elapsed
time** than Claude Code across this cohort. Three independent blinded GPT-6-Sol
judges agreed on the ordering: Kiln attempt 2 and Claude attempt 1 were strongest
and effectively tied; Kiln attempt 1 followed; Claude attempt 2 was weakest.
**Every patch still needs a correctness fix before merge.** This supports a
promising efficiency result on one task, not general output-quality parity or
superiority.

## Runs and cost

| Harness | Attempt | Anonymous candidate | Process outcome | Elapsed | Reported cost |
|---|---:|---|---|---:|---:|
| Kiln | 1 | candidate-03 | Completed | 10m13s | $3.369716 |
| Kiln | 2 | candidate-04 | Completed | 9m32s | $3.107715 |
| Claude Code | 1 | candidate-01 | Completed | 16m41s | $4.465324 |
| Claude Code | 2 | candidate-02 | Completed; runner parser rejected multiple results | 17m47s | $5.137207 |
| **Kiln total** | | | | **19m45s** | **$6.477431** |
| **Claude Code total** | | | | **34m28s** | **$9.602531** |

This follow-up used $16.07996175. With the previous $17.7736685, cumulative
reported comparison cost is **$33.85363025 of the authorized $50**. These are
provider/catalog reports, not invoices. Agent-judge billing is unavailable and
excluded from this external Opus comparison ledger, as in the previous cohort.

Claude attempt 2 exited zero and emitted two successful result events from the
same session: cumulative totals $4.962802 and $5.137207. The runner expected
exactly one result and stopped parsing after the final process had ended.
The final cumulative total is counted once. The original attempt metadata, parser
failure, and accounting adjudication are preserved; no paid retry or patch repair
was performed. This differs from the previous cohort's budget-stopped attempt.

## Code quality

All four patches passed the original **29/29 hidden acceptance cases**, build,
formatting, vet, staticcheck, targeted memory/CLI race tests, and memory e2e tests.
Seven supplemental probes exposed defects not covered by that suite:

| Confirmed defect | Kiln 1 | Kiln 2 | Claude 1 | Claude 2 |
|---|---|---|---|---|
| Destination FIFO source hangs instead of rejecting a nonregular file | Yes | Yes | Yes | Yes |
| Dry run executes writes before rolling them back | Yes | No | No | Yes |
| Accepts malformed trailing JSON (`[]]`, `[]}`) | No | No | No | Yes |
| Existing `memory --help` regresses | No | No | No | Yes |

Every candidate accepted exactly 1000 records and rejected a trailing second
JSON value (`[] []`). The malformed closing-delimiter checks were added after
review and applied uniformly; they are reported separately from the frozen
acceptance suite. The four earlier supplemental probes were declared before
this follow-up's inference began.

The FIFO finding follows each new importer into the original `sourceDigest`,
which opens the file before checking its type. A real FIFO with a valid SHA-256
binding makes every candidate exceed the three-second probe deadline. The
dry-run finding is reproduced with an abort-on-INSERT database trigger: a valid
read-only analysis fails in Kiln 1 and Claude 2 because they attempt SQL writes.
Rollback preserves stored state but does not meet the no-write contract.

Claude 2 uses `Decoder.More()` after decoding a top-level array. That rejects
another JSON value but accepts stray closing delimiters. Its broad early
dispatch of every memory command also bypasses existing help handling. The
other three patches preserve help and strictly reject the malformed delimiters.

## Three independent judges

Each judge reviewed anonymous source, diffs, submitted tests, the same contract,
and evaluator evidence in a different order. They did not receive harness
identity, cost, timing, transcripts, or each other's findings. All three used
GPT-6-Sol: this is independent agent review, **not model-family diversity or human
ground truth**. Blinding was procedural, not a filesystem security boundary.

Scores below are correctness / security / design / tests / scope, each 0–4.

| Patch | Judge 1 | Judge 2 | Judge 3 |
|---|---|---|---|
| Kiln 1 | 2 / 3 / 3 / 2 / 3 | 2 / 2 / 3 / 3 / 3 | 2 / 2 / 3 / 2 / 3 |
| Kiln 2 | 3 / 3 / 4 / 3 / 4 | 3 / 3 / 4 / 3 / 4 | 3 / 2 / 4 / 3 / 4 |
| Claude 1 | 3 / 3 / 4 / 3 / 4 | 3 / 3 / 4 / 3 / 4 | 3 / 2 / 4 / 3 / 4 |
| Claude 2 | 2 / 2 / 2 / 2 / 2 | 2 / 2 / 3 / 2 / 2 | 2 / 2 / 2 / 2 / 2 |

Judges 1 and 3 called the strongest pair “minor revisions”; judge 2 called them
“major revisions.” All agreed the FIFO contract violation must be fixed before
merge. Judge 3 explained that “minor” described the localized repair, not merge
readiness. All classified Kiln 1 and Claude 2 as major revisions. Root
adjudication treats the reproduced contract violations as blockers for every
patch, without averaging them away or rewriting the judges' assessments.

The judges found clear transaction ownership and focused implementations in the
strongest pair. They also identified a common weakness in submitted tests:
validation failures and final database snapshots did not establish rollback
after a mid-transaction write failure or prove that dry runs never attempted
writes. The external suite supplied some of that missing rollback evidence.
Concerns about credential-like keys in diagnostics remain hypothetical and are
not counted as additional confirmed defects.

Reports: [judge 1](fidelity-comparison/judge-one.md),
[judge 2](fidelity-comparison/judge-two.md),
[judge 3](fidelity-comparison/judge-three.md).

## Controls and limits

- Same frozen task and candidate baseline `40e9464` as the previous comparison;
  clean private homes, disabled personal memory/learning, no corrective feedback.
  The fixed Kiln executable was built from `d4d870d`; Claude Code remained
  `2.1.319`. Both used Opus 4.8 at high effort.
- The candidate source deliberately stays at the original baseline. It contains
  neither previous solutions/reports in its working tree nor the newer FIFO
  helper repair. This tests the fixed harness generating a solution to the same
  task; it does not measure the repaired memory helper's runtime behavior.
- Serialized order was Claude 1, Kiln 1, Kiln 2, Claude 2; equal 80-turn and
  20-minute limits. Grading and reviewer work began after timed inference ended.
  Shared warmed Go caches were retained. Cache policies and native budget
  admission still differ, and filesystem access was not sandbox-enforced.
- Claude 2 used one native Explore subagent. Its model usage lists Opus 4.8 only.
  No paid Kiln worker delegation was observed; faux routes in logs came from
  repository tests. This does not establish cross-model orchestration quality.
- All four full unit runs failed the same unchanged TUI test,
  `TestHandleSubmit_LeadingPathIsNotACommand`. A fresh unmodified baseline failed
  identically under the same grader environment. Full-suite validation remains
  failed/inconclusive in that environment, not a pass and not a candidate defect.
  Cached staticcheck and short private temporary paths were used uniformly.
- Two attempts per arm on one task cannot isolate the effect of the fidelity
  fixes. There is no evidence here that those fixes caused a code-quality
  improvement. Memory and learning were disabled, so neither was evaluated.

## Artifacts and next measurement

[Structured results](fidelity-comparison/results.json) retain exact costs,
timings, binary/task/patch hashes, per-case results, grading outcomes, native
delegation, the parser adjudication, and source-integrity checks. The directory
also contains all four frozen patches and each judge's structured scores.
Raw model traces and grading logs stay local under `/tmp/kiln-fidelity-cohort`
and `/tmp/kfr-review`; they are not published. Candidate patches were not merged.

The follow-up runner is
[`run_fidelity_comparison.py`](../eval/real-work/memory-import/run_fidelity_comparison.py);
the original runner and historical artifacts remain frozen. Offline grading uses
[`grade_fidelity_comparison.py`](../eval/real-work/memory-import/grade_fidelity_comparison.py).

The next useful comparison should use additional unseen tasks and deterministic
adversarial checks, with the same independent code review. These findings do not
justify adding a mandatory model reviewer to Kiln's normal execution path.
