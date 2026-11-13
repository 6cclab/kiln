# Comparing kiln with an external coding harness

The [2026-11-11 offline evaluation](offline-evaluation-report.md) ran 12 scripted faux scenarios and is reproducible without paid model calls. It is harness regression evidence only. It collected no Claude Code results, so it supports no cross-harness claim about quality, speed, tokens, or cost.

## Offline plan for Claude Code

Generate a manifest before spending money. This command fingerprints both
local binaries, asks each for `--version`, and hashes the selected
scenario definitions and fixture trees. It makes no model requests:

```sh
bin/kiln eval plan \
  --kiln-bin bin/kiln \
  --claude-bin claude \
  --scenarios eval/scenarios --fixtures eval/fixtures \
  --kiln-model anthropic/claude-opus-4-8 \
  --claude-model claude-opus-4-8 \
  --repeat 2 --max-usd 25 > benchmark-plan.json
```

The default cohort is `add-feature-with-tests`,
`answer-codebase-question`, `fix-bug`, and `refactor-preserving-tests`.
Two repeats produce eight paired observations and 16 runs. The manifest
pins each scenario's mechanical checks, definition and fixture hashes,
both binary hashes and versions, the exact model IDs, role mapping, and
Claude setup hash. `--only-exact` can select another explicit cohort;
the planner rejects unsupported permission modes, settings, model pins,
missing fixtures, and checks that cannot be compared mechanically. The
judge rubric in a scenario is retained in its dataset fingerprint, but
the comparison scores only mechanical checks.

The `claude-opus-4-8` ID is a requested model, not proof of the
model version that answered. Check the model version in the Claude result and keep it with the raw trace. The planner rejects a Claude model alias such
as `opus`. If the two harnesses use different
physical models, an outcome difference cannot be attributed to the
harnesses, so the planner requires the same explicit model ID in both.

The manifest allocates half the stated allowance to Claude Code and shows
a per-run `--max-budget-usd` value (for the example, `$1.5625`). This is
a Claude **per-run API limit**, not an aggregate cap for all 16 runs. Kiln's
request budget reserves a full context and output allowance before each
request; at the time of this plan, that conservative reservation for Opus
4.8 is about `$20.7`, so a `$1.5625` kiln session limit would refuse the
first request. Neither the offline manifest nor `kiln eval run` enforces
the `$25` aggregate across fresh sessions. Run a single matched pair first,
inspect actual cost and any failed-request reservations, then continue
sequentially only while the remaining approved allowance can cover the
next run. A timeout, aborted run, or budget refusal is an observation, not
evidence of task success.

Use `kiln eval run --only-exact` with the manifest's exact scenario names,
one model, and `--repeat 2`. Use fresh copies of the same fixtures for
Claude Code. Its local `--help` supports `-p`, `--output-format stream-json`,
`--verbose`, `--include-partial-messages`, `--forward-subagent-text`,
`--no-session-persistence`, `--model`, `--permission-mode`,
`--setting-sources`, `--strict-mcp-config`, and `--max-budget-usd`.
Capture raw output privately. Run in a trusted scratch directory, because `-p`
skips the trust dialog and settings validation failures can be silent.
Use the manifest's fixed setup flags and check `system/init` for the
selected model and loaded tools. The manifest's `claudeSetupHash` records
the fixed flags, settings-file bytes when provided, binary hash, model ID,
version, and Claude per-run budget. It does not pin the scratch home,
effective permissions, environment variables, other settings files,
additional flags, or loaded tools. Save these with each raw trace and audit
them before claiming the harness setups were equivalent. The manifest
is an input plan and a result consistency check, not a complete
execution recipe.

The [Claude Code programmatic-run guide](https://code.claude.com/docs/en/headless)
documents that stream JSON ends with a result containing cost metadata,
and that `system/api_retry` reports retry attempts. It does not guarantee
token usage for every failed request. `--forward-subagent-text` exposes
nested messages; it does not by itself prove a complete nested usage tally.
Audit the trace before assigning `direct`, `nested`, and `retry` counters.
If any part cannot be measured, leave the corresponding run out of the
strict comparison and report the gap. Do not enter zero for unknown usage.

Once both JSONL files are complete, validate them against the plan:

```sh
bin/kiln eval compare --plan benchmark-plan.json \
  --kiln eval/results/kiln-run.jsonl --external external-run.jsonl \
  --format json
```

The planned comparison rejects extra or missing scenario/repeat pairs,
changed fixture or dataset fingerprints, changed binary/model/setup
provenance, and changed mechanical check names or types. All source files,
raw traces, exit statuses, and measured-cost coverage should remain with
the report. Eight pairs are descriptive; they do not establish a reliable
population-level performance difference.

`kiln eval compare` compares already collected runs. It makes no model calls.
Run the same scenario definitions and fixtures through both harnesses, then
provide one external JSON object per line:

```sh
kiln eval compare --kiln eval/results/kiln-run.jsonl --external external-run.jsonl
kiln eval compare --kiln eval/results/kiln-run.jsonl --external external-run.jsonl --format json
```

External record schema, version 1:

```json
{"schema_version":1,"harness":"claude-code","harness_version":"2.0","model":"claude-sonnet","model_version":"2026-01","setup_hash":"sha256-of-harness-config","scenario":"fix-bug","repeat":1,"dataset_hash":"same-hash-as-kiln-record","ok":true,"duration_ms":1200,"checks":[{"name":"test","type":"command","pass":true}],"usage":{"direct":{"input":100,"output":20,"cache_read":0,"cache_write":0},"nested":{"input":30,"output":5,"cache_read":0,"cache_write":0},"retry":{"input":10,"output":2,"cache_read":0,"cache_write":0}},"cost_usd":0.012}
```

Use the exact `dataset_hash` from the corresponding kiln result, and only if the
external run used the same scenario, fixture and check definitions. Keep the
scenario workspace and raw harness trace separately for audit. `setup_hash`
should hash the external harness configuration, including permissions, tools,
system instructions and model routing. Record the actual harness and model
versions, not aliases that can change between runs. Repeats start at 1.

`direct`, `nested` and `retry` usage are disjoint counts. Supply all three
objects and all four counters in each; use zero only when the component was
observed to have no usage. Include subagent and retried requests when the
external trace exposes them. Some harnesses do not expose usage from failed
requests, so the reported total may be incomplete. If a component cannot be
measured, do not substitute zero: leave that run out and disclose the coverage
gap. `cost_usd` is the external source's reported run cost, if available;
verify whether it includes nested and retried requests. Omit it or use `null`
when unknown. Explicit `0` means the source reported zero, not necessarily
that an invoice confirms zero. Kiln's `cost_usd` comes from its reported usage
and catalog pricing and is an estimate, not a provider invoice. Legacy kiln
records with `cost_usd: 0` are treated as unknown because that field did not
distinguish missing pricing from a true zero.

Comparison requires an exact scenario/repeat cohort, matching dataset hashes,
and identical named mechanical check sets and types. A run that exited with
`ok: false` scores zero. Quality is the fraction of passing mechanical checks;
LLM judge scores are excluded because judges and rubrics may differ. The
report shows mean quality, elapsed milliseconds, tokens and mean cost over
pairs where both costs are known. Individual and paired known-cost counts
accompany cost means. Inspect the
per-pair JSON for the observations and cost gaps. These descriptive
numbers do not establish statistical superiority.

The external format intentionally has no kiln policy, binary or internal run
provenance fields. The personal-policy promotion gate's
required provenance checks reject it, and it cannot be imported as a kiln eval record.
