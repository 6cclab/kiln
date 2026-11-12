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
- Per-worker shell process ownership: completion and cancellation stop the
  worker's tracked Unix process groups without stopping parent or sibling jobs.
  Commands cannot start after their worker scope closes. This is a prerequisite
  for isolated shell workers, not shell sandbox support itself.
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
  Opt-in discovery for literal local Go package tests adds a bounded partial
  package/metadata snapshot, including the absence of optional workspace files.
- Opt-in clean-checkout editing worktrees, structured retained patch artifacts,
  and explicit digest/base-checked patch application under a repository lock.
  Batch preflight/application combines disjoint artifacts from one clean base.
  Opt-in dirty-parent snapshots preserve staged/unstaged/untracked inputs and
  export only worker changes; integration requires the captured baseline to match.
- Descriptor-relative isolated file operations on macOS/Linux reject symlink and
  hardlink aliases and fail closed after cleanup. Windows isolated editing fails
  explicitly pending a corresponding implementation.
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
- Model-invocable personal, project and plugin custom commands join the skill
  catalog in slash-registry precedence, with exact-name invocation; a command
  that disables model invocation shadows a same-named entry instead of exposing it.
- [Automatic patch integration](efficiency/patch-integration.md): with the user's
  own `orchestration.autoIntegrate` opt-in, a fully committed tool batch of
  isolated-worker artifacts is applied all-or-none through the existing batch
  preflight under the repository lock. Overlaps, a moved baseline or any failed
  call apply nothing and name the conflict. A recorded intent is reported as
  uncertain after a restart and never replayed.
- [Broader Go verification discovery](efficiency/verify-discovery.md): in-module
  transitive imports (parsed statically), local `replace` targets and module-root
  `go test ./...`, within the shared limits. Evidence stays `go-partial-v1` and
  never justifies skipping the requested command.
- [Progress-loop guard](efficiency/progress-loop.md): opt-in
  `runtimeEfficiency.progressLoopLimit` notices repeated unchanged reads,
  repeated read-only sequences (including conservatively parsed Bash against a
  bounded worktree snapshot) and edit/revert cycles, then escalates through the
  existing guard. Forwarded to nested workers; no model calls.
- [Verified fact extraction](efficiency/fact-extraction.md): with learning and
  personal memory enabled, an observed successful literal local Go test command
  becomes an attributed, expiring project-memory procedure only after its cited
  file snapshot is rechecked; everything else stays a dismissible review candidate.
  Run counters also produce reviewable, unevaluated improvement proposals
  (`kiln learning proposals`) that never change policy.
- [Context selection reasons](efficiency/context-reasons.md): `/context` shows
  why each skill, agent, MCP tool and memory item is in or out of the request,
  and reports model, effort, permission mode and auto-memory directory with
  their sources, as does `kiln config`.
- Readiness fixes: crash recovery never replays an effect-pending tool and print
  mode stops on an unresumable operation ([runtime](efficiency/readiness-runtime-fixes.md));
  Windows installs keep `kiln.exe`, `kiln doctor` flags an unusable model, and
  releases wait for checks and native install smoke
  ([distribution](efficiency/readiness-distribution-fixes.md)).

The integrated tranche is described in
[readiness integration](efficiency/readiness-integration.md). Earlier details are in
[worker confinement and verification discovery](worker-confinement-verification-discovery.md)
and [worker snapshots and verification dependencies](worker-snapshot-verification-improvements.md).

## Still outstanding from the full plan

- Hooks and other instruction types in unified retrieval; the catalog covers
  model-invocable skills and custom commands, dispatchable agents and eligible MCP tools.
- Shell-capable isolated workers. `orchestration.isolatedShell` is refused on
  every platform before a child starts: a `setsid()` descendant can outlive the
  worker's process-group scope and worktree cleanup, and no enforceable descendant
  ownership exists yet ([sandbox shell](efficiency/sandbox-shell.md)). The macOS
  Seatbelt profile is tested directly only. Linux still needs host Unix socket
  protection; Windows isolated editing fails explicitly.
- A full verification dependency graph and selective test policies. Discovery does
  not trace external or workspace modules, build tags, cgo, toolchain or
  environment, or non-Go commands; Windows cross-volume handling is cross-compiled
  but not run natively. Nothing proves an unchanged check can be skipped.
- Fact extraction beyond a single literal Go test command launched at the project
  root; directory and missing-file dependencies stay review-only. Policy candidates
  still require explicitly run evaluation; nothing schedules paid evaluations.
- Broader file/result reuse. The progress guard excludes effectful Bash, files
  outside the working directory or over 1 MiB, symlinks and hook-rewritten edit paths.
- `/context` cannot recover per-request suggestions, retrieval decisions or search
  budget drops after a restart, and the integrated binary has not had a
  real-terminal `kiln-qa` drive of the new view.
- Broader head-to-head evaluation with complete external usage attribution.

A policy promotion is evidence about the evaluated tasks, not proof that the full
harness outperforms another product. The implementation keeps incomplete behavior
explicit in configuration documentation.
