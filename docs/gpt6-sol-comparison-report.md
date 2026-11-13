# GPT-6-Sol: Kiln versus Codex

Codex completed the controlled pair **faster and more cheaply**: Kiln took 16.0%
longer and used 73.4% more estimated model cost. Neither generated patch is ready
to merge. Kiln passed more hidden acceptance cases but hangs on a FIFO source;
Codex rejects project imports based on untrusted input scope. This same-model
comparison exposes different harness outcomes, not a general quality winner.

## Controlled pair

| Harness | Candidate | Outcome | Elapsed | Catalog-equivalent cost |
|---|---|---|---:|---:|
| Codex 0.167.1 | candidate-01 | Completed | 11m30s | $1.281160 |
| Kiln d4d870d | candidate-02 | Completed | 13m20s | $2.221556 |

Both ran concurrently on the same machine with GPT-6-Sol at high effort,
a 272,000-token context configuration, the same task and original baseline
`40e9464`, scratch homes, and delegation disabled. There was no corrective
feedback, and both patches were frozen before grading. Wall times include
startup, model calls, tools, self-directed tests, and shared-machine
contention. One paired task cannot establish parity or general superiority.

## Correctness and test quality

| Evidence | Codex | Kiln |
|---|---|---|
| Frozen hidden acceptance | 27/29 | 29/29 |
| Supplemental probes | 7/7 | 6/7 |
| Build, formatting, vet, staticcheck | Pass | Pass |
| Memory e2e | Pass | Pass |
| Original full unit / targeted race | Fail | Fail |
| Targeted unit / race with canonical temporary path | Pass | Pass |

Codex's two hidden failures share one cause. `internal/memory/import.go:67`
checks whether the **input** scope equals `user:preferences` and rejects a fact
or source-bearing record even when importing into a project. The contract says
imported scope does not determine destination identity. A project import of an
otherwise valid fact tagged with that scope fails; the same mistake also blocks
replacement. This is overvalidation, not a cross-scope write or privacy leak.
Ordinary exports through the existing Put API cannot create that combination,
but the explicit untrusted-scope contract still requires handling it.

Kiln's `internal/memory/import.go:203` calls the baseline `sourceDigest`, which
opens a file before inspecting its type. A destination FIFO with a valid-shaped
source binding exceeds the three-second probe deadline instead of being rejected.
Codex prechecks file type and passes this probe. Both patches reject malformed
trailing JSON, preserve help, accept exactly 1000 rows, and avoid attempted memory
writes during dry-run. Both contain meaningful transaction/rollback tests.

Both sets of new tests also compare temporary paths without normalizing macOS's
`/tmp` to `/private/tmp` alias, so their original unit and race runs fail on
those path assumptions. With only TMPDIR canonicalized, the unchanged source passes
both targeted unit and race suites. These are test-portability defects, not
additional importer runtime failures. The full unit runs also fail the unchanged
TUI test `TestHandleSubmit_LeadingPathIsNotACommand`; an earlier unmodified
same-baseline control reproduced that failure under the same grader environment.
The original failures are retained, and the full suites are not reported as passing.

## Three independent judges

Each judge reviewed anonymous patches, source, submitted tests, the same contract,
and evaluator evidence. They did not receive identity, cost, timing, model traces,
or other reviews. Review orders were 01/02, 02/01, and 01/02. All used GPT-6-Sol:
independent reviews, not model-family diversity or human ground truth. Blinding
was procedural, not enforced by filesystem isolation.

Scores are correctness / security / design / tests / scope, each 0–4.

| Patch | Judge 1 | Judge 2 | Judge 3 |
|---|---|---|---|
| Codex | 3 / 4 / 3 / 3 / 3 | 2 / 3 / 3 / 2 / 3 | 3 / 4 / 3 / 3 / 3 |
| Kiln | 2 / 2 / 3 / 2 / 3 | 2 / 2 / 3 / 2 / 4 | 3 / 2 / 3 / 3 / 4 |

All judges require a fix in both patches. All classify Codex as minor revisions;
judges 1 and 2 classify Kiln as major revisions, while judge 3 calls its repair
minor but mandatory. Each identifies the FIFO hang as the more serious
availability/security issue. Judge 1 explicitly favors Codex as the safer base;
the others distinguish its safer source handling from Kiln's better scope fidelity.
Root adjudication retains that disagreement and treats both reproduced contract
violations as merge blockers, without converting scores into a quality winner.

Reports: [judge 1](gpt6-sol-comparison/judge-one.md),
[judge 2](gpt6-sol-comparison/judge-two.md),
[judge 3](gpt6-sol-comparison/judge-three.md).

## Cost and cache observations

| Reported tokens | Codex | Kiln |
|---|---:|---:|
| Uncached input | 135,011 | 880,114 |
| Cached input | 3,589,888 | 1,314,688 |
| Output, including reasoning | 29,316 | 19,839 |
| Cached share of all input | 96.4% | 59.9% |

Kiln used fewer total input and output tokens, but substantially more uncached
input. That accounts for the higher catalog cost. This measurement does not
identify why cache reuse differed: request construction, prefix stability,
conversation/tool behavior, and provider cache state need separate inspection.
There were 66 observed Codex usage increments and 43 Kiln usage records; these
are recorded usage events, not evidence about unreported retries.

Both used the same ChatGPT-authenticated account, and only GPT-6-Sol was observed.
No delegated session was observed. Cost uses the recorded token categories and
catalog rates, not an invoice or a subscription charge. Cached input is included
in Codex's total input but separate from Kiln's uncached input, and reasoning is
already included in output. Neither is double counted. Model/rate reference:
[OpenAI GPT-6-Sol documentation](https://developers.openai.com/api/docs/models/gpt-6-sol).

## Interrupted initial experiment and budget

Before the controlled pair, an initial Codex run completed in 14m03s for
$1.7601292. A scheduling amendment then overlapped two Kiln attempts. Our external
budget monitor used an overly conservative $5.728 request reserve and terminated
both after about 8m08s, at $1.4973192 and $0.986404 respectively. This was a
benchmark-driver failure, not a model or harness-quality failure. The planned
second Codex attempt never ran. These three patches and original metadata are
retained separately and excluded from the controlled quality comparison.

The interrupted Kiln session audit found both executing local Bash tools, with
model response usage committed and no pending model stream or worker session
observed. The corrected pair used independent $5.951258 allocations and a common
$3.28 full-request reserve, without adding mutually exclusive input categories.
Both finished naturally without watchdog intervention. External cost monitoring
is approximate, not an invoice-level guarantee.

| Ledger | Estimated cost |
|---|---:|
| Earlier comparison cohorts | $33.85363025 |
| Initial GPT attempts, including interrupted work | $4.24385240 |
| Controlled parallel pair | $3.50271520 |
| **Cumulative** | **$41.60019785 / $50** |

Agent-judge billing is unavailable and excluded from this external-model ledger,
as in the previous cohorts. No further paid implementation retries were made.

## Artifacts and implications

[Structured results](gpt6-sol-comparison/results.json) retain exact cost, timing,
token counts, hashes, per-case results, original grading failures, canonical-path
reruns, and source integrity. The directory contains the two frozen patches,
three judge reports, and the initial interrupted experiment. Raw model traces,
credentials, and private configuration are not published. Candidate patches
remain evaluation artifacts and were not merged.

The candidate baseline deliberately predates the memory FIFO helper repair.
The fixed Kiln executable generates a patch against the original task; this is
not a test of the already-repaired helper's runtime behavior. Personal memory
and learning were disabled. Native tools and prompts differ, while model identity
and effort are matched. This controls an important variable but does not isolate
which harness mechanism caused either defect, or eliminate model randomness.

The next useful work is to inspect Kiln's cache reuse and run additional unseen
paired tasks. Preserve adversarial deterministic checks and independent review
for measurement; these results do not justify a mandatory model-review stage in
the normal harness path.

Offline tooling: [controlled runner](../eval/real-work/memory-import/run_gpt6_controlled.py),
[grading adapter](../eval/real-work/memory-import/grade_gpt6_comparison.py),
and [accounting regression checks](../eval/real-work/memory-import/test_gpt6_accounting.py).
The initial runner and scheduling amendment are retained as historical experiment
code; do not replay them against their fixed scratch directories.

Repository validation for this report/tooling change: Go 1.26.6 `make check` and
all three offline accounting regression tests passed. No product Go or UI code
was changed. Candidate source and frozen patch integrity checks passed.
