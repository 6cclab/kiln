# Efficiency implementation status

This tracks the accepted efficiency plan and prevents partial capabilities from
being mistaken for the complete roadmap.

## Implemented

- Bounded unified skill, agent and MCP discovery, exact-name skill invocation, explicit
  MCP admission, automatic metadata suggestions, catalog/search-only modes,
  pins and exclusions, and effective configuration output. Enabled plugin skills, agents
  and plugin MCP tools participate; legacy searches remain callable.
- Persistent nested-worker usage with per-model breakdowns; auxiliary usage no
  longer replaces the parent's context-occupancy estimate.
- Full-request wire-size evaluation checks, policy/binary/fixture fingerprints,
  and atomic shared model-call/token/catalog-cost admission reservations.
- Shared worker concurrency, dispatch-count, depth and timeout controls.
- Opt-in conservative routing, new-task reassessment, and tool-failure escalation.
- Private SQLite project memory with lexical retrieval, attribution, correction
  history, expiry, forgetting, export, restore and worktree-aware source fingerprints.
  Explicit global preferences share the retrieval allowance; project keys take precedence.
- Narrow evaluated personal policy proposals, promotion gate, runtime application,
  history and rollback; evidence-derived candidates with inspectable cost estimates
  and validated held-out manifests. Candidate promotion rechecks exact scenario
  sets, fixture separation and provenance. Evaluation is explicitly run, not a background job.
- Durable accepted todo checkpoints, branch-aware restoration and bounded
  reminders after compaction; failed calls never overwrite accepted progress.
- Structured objective/constraint/decision checkpoints and command evidence
  captured against permitted file snapshots, rechecked for freshness on recovery.
  A bounded verification-status tool reports dependency changes and invalidates
  superseded evidence without executing or skipping checks. Explicit directory
  dependencies include membership changes; an optional plan returns prior commands
  to rerun or evidence needing review without making safe-to-skip claims.
- Opt-in clean-checkout editing worktrees, structured retained patch artifacts,
  and explicit digest/base-checked patch application under a repository lock.
  Batch preflight/application combines disjoint artifacts from one clean base.
  Opt-in dirty-parent snapshots preserve staged/unstaged/untracked inputs and
  export only worker changes; integration requires the captured baseline to match.
- Offline exact-cohort comparison with external harness records, including
  strict usage accounting, setup provenance, mechanical quality, and paired costs.
  `eval plan` fingerprints a selected Claude Code cohort without model calls;
  `eval compare --plan` validates its recorded provenance and check set.
- Opt-in bounded read formatting reuse with fresh-content checks and a repeated
  identical-error and multi-call failed-cycle guards, all forwarded to nested workers.
- Per-field provenance for the efficiency configuration report, including defaults,
  explicit overrides and evaluated policy sources.
- Opt-in bounded local run aggregates and dismissible review hypotheses about
  tool failures and context use; collection adds no model calls or prompt context.
  Repository settings cannot enable capture, undo a user's memory/learning opt-out,
  increase memory retrieval or outcome retention, or relax user request budgets.
- Shared patch validation for single and batch worker inspect/apply, and retained
  evaluation artifacts whose recorded paths survive scratch-directory cleanup.
- A small [live Opus 4.8 comparison](live-comparison-report.md): four tasks,
  two repeats per arm, all 52 mechanical checks passed. This is descriptive
  evidence; external retry attribution remains incomplete.
- A [real-repository code-quality comparison](real-work-comparison-report.md):
  two memory-import attempts per harness, hidden acceptance, independent blind
  reviews, and reproduced counterexamples. All four pass 29 acceptance cases
  but require revisions; candidate patches remain evaluation artifacts.
- An [offline harness-fidelity audit](harness-fidelity-audit.md) with regressions
  for omitted nested instructions, lost Anthropic redacted replay data, and
  FIFO blocking in memory source validation. These are specific correctness
  repairs; generated-code quality parity remains unproven.
- A [post-fix real-work repeat](fidelity-comparison-report.md) with three blinded
  judges: both harnesses completed twice, and Kiln used 32.5% less reported cost
  and 42.7% less time. Judges tied the strongest Kiln and Claude patches; all
  four still need correctness fixes. Comparison spend through that cohort was $33.85.
- A [same-model GPT-6-Sol Kiln/Codex pair](gpt6-sol-comparison-report.md), run
  concurrently with delegation disabled and three blinded judges. Codex was
  faster and cheaper; Kiln passed more hidden cases, but its FIFO hang and
  Codex's imported-scope rejection both block merge. Cumulative estimated external
  comparison spend is $41.60/$50, including interrupted driver attempts.
- [Request fidelity improvements](request-fidelity-improvements.md): dynamic
  context preserves the conversation prefix; hook feedback survives compaction
  and retries; Responses adapters retain text identity/phase; USD reservations
  avoid double-counting disjoint input categories. Local regressions cover these
  changes; their effect on live cost and generated-code quality is not measured.

Details and local performance evidence for the latest tranche are in
[worker snapshots and verification dependencies](worker-snapshot-verification-improvements.md).

## Still outstanding from the full plan

- Plugin commands, hooks and other instruction types in unified retrieval; the
  current catalog covers model-invocable skills, dispatchable agents and eligible MCP tools.
- General-purpose isolated workers with shell/sandbox support and automatic
  integration of concurrent patches. Current isolated workers support file edits
  from clean HEAD or an opt-in dirty snapshot and require explicit application.
- Automatic verification dependency discovery and broader selective test policies;
  current evidence and rerun planning cover explicitly declared files/directories,
  not whole-task correctness or proof that unchanged checks can be skipped.
- Verified automatic fact extraction and background policy-candidate generation
  from ordinary task outcomes. Run statistics produce review hypotheses only;
  policy candidates still require explicit evaluation evidence, and plans do not
  schedule or execute paid evaluations.
- Broader file/result reuse, progress-loop detection beyond repeated failure sequences,
  selective verification policies, and reviewable skill/harness improvement proposals.
- Provenance outside the efficiency controls and detailed selection reasons in
  `/context`; broader head-to-head evaluation with complete external usage attribution.

A policy promotion is evidence about the evaluated tasks, not proof that the full
harness outperforms another product. The implementation keeps incomplete behavior
explicit in configuration documentation.
