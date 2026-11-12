# Kiln and Claude Code live cohort — 2026-11-11

The user approved a paired, capped run of Claude Opus 4.8 through kiln and Claude Code. Both arms completed two repeats of four coding scenarios. The [sanitized per-run results](live-comparison-results.json) preserve observed wall times, reported costs, mechanical check outcomes, the frozen kiln binary hash, and scenario, fixture, and settings hashes. The Claude Code executable digest was not captured at run time and is recorded as `null`; CLI version `2.1.319` was observed. Raw traces and workspaces remain local; `/tmp/kiln-opus48-cohort-path` points to the cohort directory, and `/tmp/kiln-opus48-pilot-path` points to the first pilot pair.

## Method

- Scenarios: `fix-bug`, `add-feature-with-tests`, `refactor-preserving-tests`, and `answer-codebase-question` from `eval/scenarios`, each with two repeats. Each arm started from a separate copy of the corresponding fixture in `eval/fixtures`. Both arms used `claude-opus-4-8` and the scenario prompt and bypass permission mode.
- Kiln used the frozen binary with SHA-256 `e4432db1ec97bd58b2abd15acb40314517e872a5a4998ff618a8896cc02e144e`, `-p --output-format json --model anthropic/claude-opus-4-8`, scenario `--max-turns`, and a request budget with `maxCalls` equal to the scenario turn cap and `maxUSD` at most 22. Personal memory and learning were disabled. WebSearch was denied.
- Claude Code CLI version `2.1.319` used `-p --output-format stream-json --verbose --model claude-opus-4-8 --max-budget-usd 1 --no-session-persistence --strict-mcp-config`. WebSearch was denied.
- Each run used a separate scratch HOME. Claude Code used a scratch `CLAUDE_CONFIG_DIR` for each run. OAuth tokens were passed to the child process through environment variables and are absent from the saved commands and sanitized results. Runs were serialized. Repeat 1 ran kiln before Claude Code; repeat 2 reversed the order. The first `fix-bug` pair was the pilot.
- Model identity was checked after collection: all eight Claude Code streams named `claude-opus-4-8` in both init and assistant message fields; all eight kiln session-start logs named `anthropic/claude-opus-4-8`. The latter verifies kiln's selected route, while the Claude stream fields report the model on returned messages. These evidence types are listed per row.
- The same `scenario.Load` and `eval.RunChecks` code graded both post-run workspaces. For Claude Code, assistant `tool_use` IDs and the final `result` event were normalized into the checker input. No live judge score was used. Tool usage from nested agents and retries was not inferred or filled with zero.
- The user cap was $25 total reported spend. Before each kiln run the request budget was bounded by the remaining cap minus a $1 Claude allowance; the runner required room for a $20.70 full-context request. Each Claude run had a $1 CLI cap. Any failed, timed-out, or unpriced run would have stopped further paid calls. The cohort completed at **$1.1484105 total reported cost**. This is model-reported/catalog cost, not an invoice.

## Observations

| Arm | Runs | Mechanical checks | Mean wall time | Total reported cost | Mean reported cost |
|---|---:|---:|---:|---:|---:|
| Kiln | 8 | 26/26 | 8,239 ms | $0.2588 | $0.0324 |
| Claude Code | 8 | 26/26 | 12,086 ms | $0.8896 | $0.1112 |

| Scenario | Repeat | Kiln wall time | Claude Code wall time | Kiln reported cost | Claude Code reported cost |
|---|---:|---:|---:|---:|---:|
| fix-bug | 1 | 4,914 ms | 9,227 ms | $0.04817 | $0.10178 |
| fix-bug | 2 | 5,187 ms | 7,624 ms | $0.02133 | $0.09036 |
| add-feature-with-tests | 1 | 9,840 ms | 12,205 ms | $0.03011 | $0.11038 |
| add-feature-with-tests | 2 | 8,541 ms | 12,359 ms | $0.02997 | $0.11010 |
| refactor-preserving-tests | 1 | 14,181 ms | 18,317 ms | $0.04505 | $0.14239 |
| refactor-preserving-tests | 2 | 11,986 ms | 21,363 ms | $0.03764 | $0.15581 |
| answer-codebase-question | 1 | 5,339 ms | 7,740 ms | $0.02249 | $0.08955 |
| answer-codebase-question | 2 | 5,920 ms | 7,851 ms | $0.02406 | $0.08924 |

All four scenario fixture hashes and scenario hashes match across arms and repeats. All 16 processes exited successfully, and both arms passed every named mechanical check. The recorded wall times include CLI startup, model requests, tool execution, and host scheduling. Other repository checks ran on the host during this cohort, so host load was uncontrolled. The $1 Claude cap and kiln request budget are different mechanisms, although neither constrained a completed run. The scenario set is small and its checks do not exhaustively assess code quality; in particular, a passing refactor test does not prove all behavior is preserved. The observed timing and reported cost differences are descriptive for these runs only and do not establish a general advantage.

The external comparison importer requires disjoint direct, nested, and retry token counts. Claude Code's result stream does not establish that attribution for every run, so no schema version 1 external file was fabricated and `kiln eval compare` was not used. Cost figures are each arm's reported total; provider billing may differ.
