# Offline evaluation run — 2026-11-11

This run checked kiln's scripted harness behavior. It did not call a paid model or Claude Code, and it is not a model quality or harness efficiency comparison with Claude.

## Reproduce

Run from the repository root. The output directory is temporary, so this does not overwrite `eval/results/baseline.jsonl` or install kiln globally.

```sh
eval_dir=$(mktemp -d /tmp/kiln-offline-eval.XXXXXX)
mkdir -p "$eval_dir/home" "$eval_dir/results"
go build -o "$eval_dir/kiln" ./cmd/kiln
HOME="$eval_dir/home" HARNESS_OFFLINE=1 "$eval_dir/kiln" eval run \
  --kiln-bin "$eval_dir/kiln" \
  --scenarios eval/scenarios --fixtures eval/fixtures \
  --results "$eval_dir/results" \
  --models faux/faux-1 \
  --roles fast=faux/faux-1,heavy=faux/faux-1 \
  --repeat 1 -j 4 --keep
HOME="$eval_dir/home" HARNESS_OFFLINE=1 "$eval_dir/kiln" eval report \
  --results "$eval_dir/results" --format json
```

The captured run is at `/tmp/kiln-offline-eval.P5X0Zk/results/20261111-130319-a38c70eb.jsonl`. Its binary SHA-256 is `f1651776248bbc785e447ce11d47dc5c93e1c6cb50e7c00d6e4c8313f008d078`. The repository contained uncommitted work when this binary was built; the hash identifies this exact build, while the command above reproduces the procedure against the current source.

## Observations

| Measure | Result |
|---|---:|
| Scenario runs completed | 12 / 12 |
| Mechanical checks passed | 37 / 37 |
| Mean combined scenario score | 0.9825 |
| Reported input tokens | 87,012 |
| Reported output tokens | 1,829 |
| Sum of recorded run durations | 7,193 ms |

Eleven scenarios used `faux/faux-1`; `long-context-compaction` is pinned by its scenario to `faux/faux-2`. Six scenarios include canned faux judge verdicts. Those verdicts contributed to the combined scores, including three scores of 0.97 and two scores of 0.94 despite every mechanical check passing. The scripted server decides the tool calls and responses, so the successful checks show that kiln can execute those scripted paths and observe the expected files, permissions, and events. They do not show that any real model can choose the correct actions.

The result records report zero cost for most scenarios and `$0.24165` for the faux compaction scenario. Faux requests incurred no provider bill; these values are software estimates and are not suitable for cost comparisons. The records' `usage.totalTokens` field is zero even when `usage.input` and `usage.output` are positive; the totals above sum the component counters directly. The duration sum is not suite wall time because four workers ran concurrently.

With `--keep`, retained session and log copies are under `results/runs/<run-id>/...`. This captured run predates the fix for stale `artifacts.session` and `artifacts.log` values in its JSON records, so use the retained copies for its trace review. Subsequent runs record the retained paths.

## Comparison status

The fixtures and mechanical checks can be reused to construct a paired comparison, but no Claude Code run is present. `kiln eval compare` requires matching scenario and repeat cohorts, dataset hashes, and named check sets before it will report paired results. The current faux transcripts cannot stand in for a Claude Code baseline: their actions and judge verdicts are predetermined. A representative comparison still needs independently collected Claude Code traces with the same starting fixtures, prompts, permissions, check definitions, repeat counts, measured direct/nested/retry usage, and actual model and harness versions. The existing 12 scenario set is a small functional sample, so even a paired run would support descriptive results rather than broad claims of superiority.

## Worker integration audit

The batch path validates retained manifests and patch digests, uses Git's NUL-delimited patch path parser, rejects unsafe modes and path overlaps (including casefolded and hard-link aliases), checks the combined patch, and rechecks the clean base under the kiln integration lock before applying. Renames and copies are conservatively unsupported. A validation failure leaves the parent untouched. The lock coordinates kiln processes only; an external edit, process crash, cancellation, or filesystem error during the final Git apply has no rollback guarantee.
