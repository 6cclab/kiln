# Real repository memory-import comparison

This is a frozen, one-feature evaluation of Kiln and Claude Code using Opus 4.8,
with two attempts per harness. It measures patch quality as well as reported
cost and elapsed time. It is separate from the small fixtures in `eval/scenarios`.

- `task.md`: identical implementation prompt for both harnesses.
- `rubric.md`: predeclared review dimensions and fairness constraints.
- `hidden_acceptance.py`: 29 independent CLI cases, frozen before inference.
- `test-method.md`: acceptance-suite coverage and limitations.
- `reviewer-instructions.md`: instructions for two independent blinded reviews.
- `run_comparison.py`: frozen, cohort-specific preparation and paid runner.
- `grade_comparison.py`: offline anonymization, build, and grading.
- `supplemental_probes.py`: post-submission reviewer counterexamples, reported
  separately from the predeclared suite.

The paid runner deliberately pins commit `40e9464`, executable versions, task
hashes, order, and budget accounting for this particular experiment. It is not a
reusable spending service: the $50 ledger includes $1.1484105 from an earlier
cohort, and rerunning it requires a new explicitly authorized experiment. Its
`prepare` command makes no model calls; only `run --live` loads the local
Anthropic OAuth credential. Credential values are not saved in command files.
Provider-reported/catalog costs and CLI caps are not invoice-level guarantees.

Run offline acceptance against a candidate binary:

```sh
python3 eval/real-work/memory-import/hidden_acceptance.py --binary /absolute/path/to/kiln
python3 eval/real-work/memory-import/supplemental_probes.py --binary /absolute/path/to/kiln
```

Full grading must wait until timed inference finishes. `grade_comparison.py
prepare --runs-finished` normally requires four completed results. After auditing
a stopped cohort, `--include-stopped-attempt` also preserves a terminal failed
attempt as an explicitly incomplete candidate; it does not turn it into a
successful run or authorize more paid calls. See `--help` for output paths.
Preparation creates independent anonymous baseline
clones with the frozen patches applied. Keep its identity map and binary output
root outside the reviewer bundle tree. `grade --runs-finished` builds each
candidate and records formatting, vet, staticcheck, full unit, targeted race,
memory e2e, and hidden acceptance outcomes. It checks that source snapshots and
patch bytes remain unchanged. Failures must be retained, not repaired and
silently rescored.

For this cohort, the first offline pass hit two grader environment problems:
the pinned staticcheck release name needed a lookup that `GOPROXY=off` blocked,
and the long temporary directory exceeded a sandbox test's Unix socket path
limit. `repair-infrastructure` retains the initial failures and adds uniform
reruns using the cached canonical module version (verified as staticcheck
2026.2.2), a local file-only module proxy, and short private temporary paths.
It records source integrity again. These reruns make no model or network calls.

Reviewers see the task, rubric, anonymous code and objective results in opposite
orders. They do not see harness identity, prices, timings, model transcripts, or
each other's reviews until their independent assessments are complete. Their
scores are agent-assisted judgments, not human ground truth. One feature and
two attempts per arm cannot establish general harness superiority.

## Authorized post-fidelity follow-up

The [follow-up report](../../../docs/fidelity-comparison-report.md) retains the
same task baseline and Opus settings, with Kiln executable revision `d4d870d`
and three independent blinded GPT-6-Sol judges. Its separate
`run_fidelity_comparison.py` preserves this cohort's exact paid runner, including
the single-result parser limitation encountered after Claude's final process
exited successfully. No paid retry was made; accounting uses its last cumulative
success result once. This runner is frozen evidence, not a reusable service.

`grade_fidelity_comparison.py` applies the prior cohort's offline infrastructure
corrections uniformly from the first pass. `supplemental_fidelity_probes.py`
contains post-review malformed-closing-delimiter cases, separate from the frozen
29-case acceptance suite. Both make no model calls.

The follow-up ledger begins at $17.7736685 and ends at $33.85363025 of the $50
authorization. Further paid runs require a new cohort ledger that accounts for
all existing spend; do not rerun either historical runner as though its starting
balance were current. Frozen candidate patches are evaluation artifacts only.
