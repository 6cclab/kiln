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

- Efficiency wave (one PR per item):
  - [Isolated worker shells](efficiency/isolated-shell-activation.md) on Linux:
    a bwrap private root, host Unix sockets hidden, and synchronous teardown
    through a pidfd on the namespace init. If the namespace can't be confirmed
    empty, the worktree is kept and dispatch fails. Hosts without pidfd or
    `--info-fd` are refused.
  - [Verification discovery](efficiency/verify-discovery.md) (`go-partial-v2`)
    traces `go.work` modules, counts build-tagged and cgo files, and records the
    Go environment variables that select or compile files.
  - [Fact extraction](efficiency/fact-extraction.md) works when kiln is launched
    from a subdirectory, and facts can cite directory and absent-file sources.
  - The [progress guard](efficiency/progress-loop.md) tracks hook-rewritten
    paths and in-tree symlinks, and has a configurable file cap. Behind
    `readCache`, it replaces unchanged read, grep and glob results with a
    pointer to the earlier call.
  - [`/context` reasons](efficiency/context-reasons.md) persist with the
    session and survive a restart.

The integrated tranche is described in
[readiness integration](efficiency/readiness-integration.md). Earlier details are in
[worker confinement and verification discovery](worker-confinement-verification-discovery.md)
and [worker snapshots and verification dependencies](worker-snapshot-verification-improvements.md).

## Still outstanding from the full plan

- Hooks in unified retrieval are out of scope on purpose. Claude Code never
  exposes hooks to the model, so indexing them would break parity. The catalog
  covers model-invocable skills and custom commands, dispatchable agents and
  eligible MCP tools.
- Isolated worker shells on macOS and Windows: both are refused. On Linux:
  - sockets created in `/etc` or the Git dirs after the per-command scan are
    still reachable;
  - Git metadata is readable;
  - tools under `$HOME` or `/opt` are not found;
  - a worktree kept after an unconfirmed teardown must be removed by hand;
  - hosts that restrict user namespaces are untested (they get a refusal).
- A full verification dependency graph and selective test policies. Discovery
  does not cover:
  - environment variables outside the recorded set, `go env -w` / `GOENV` files,
    the `go` binary or the module cache;
  - imports of workspace modules outside the root, or ancestor `go.work` files;
  - non-Go commands.

  Nothing proves an unchanged check can be skipped.
- Fact extraction still covers only one literal Go test command. Evidence
  outside the project root, symlinked or special directory entries, and
  oversized directories stay review-only. Policy candidates still need an
  explicitly run evaluation; nothing schedules paid evaluations.
- The progress guard still excludes effectful Bash and files outside the working
  directory. `/context` restores only the last request's decisions. It has not
  had a real-terminal `kiln-qa` drive.
- Broader head-to-head evaluation with complete external usage attribution.

A policy promotion is evidence about the evaluated tasks, not proof that the full
harness outperforms another product. The implementation keeps incomplete behavior
explicit in configuration documentation.
