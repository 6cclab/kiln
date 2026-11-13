# Efficiency roadmap: Orca coordinator brief

You are the **coordinator** for a supervised Orca Run. You plan, dispatch, verify and report,
and you never edit source files yourself. Workers are Codex agents on `WORKER_MODEL`, each in
its own child worktree.

- `WORKER_MODEL` = `gpt-6-sol` (swap for `gpt-6.1-sol` here if wanted; pass no `--effort`, so
  the model default applies)
- Repo: `/Users/andrepato/projects/harness-kiln`, branch `feat/efficient-orchestration-memory`
- Source of truth for scope: `docs/efficiency-implementation-status.md` → "Still outstanding"

## 0. Load the protocol first

1. `orca skills get orchestration` and follow it exactly (Run, Task, Dispatch, check/ack loop,
   release). If any `orca` call fails with `runtime_access_denied`, re-run it with escalated
   permissions; never run `orca open` or restart Orca for that error.
2. Load `orca skills get orchestration --reference references/coordinator-loop.md` and
   `--reference references/placement-and-remote.md` (you create worktrees and pick a model).
3. Read `AGENTS.md`, `docs/efficiency-implementation-status.md`,
   `docs/worker-confinement-verification-discovery.md`.

## 1. Constraints

- **Another Codex session is editing this checkout right now.** Wave 1 must not touch its
  uncommitted files (see `git status`): `internal/agent/dispatch.go`,
  `internal/agent/worker_batch*.go`, `internal/claude/commands/**`,
  `internal/claude/settings/{efficiency_sources,orchestration}*.go`, `internal/cli/discovery.go`,
  `internal/cli/learning*.go`, `internal/cli/command_discovery*.go`, `internal/learning/**`,
  `internal/memory/store.go`, `internal/tools/capability_search*.go`, `internal/tools/skill*.go`.
  Never run `git stash`, `checkout`, `reset` or `clean` in this checkout.
- Out of scope for this Run: plugin commands/hooks in unified retrieval (the in-flight session
  owns it), and any paid head-to-head evaluation (`make eval-live`, live comparisons). The
  external comparison budget is at $41.60 of $50.
- Workers commit on their own worktree branch. Nobody pushes, opens a PR, or merges into
  `feat/efficient-orchestration-memory`; integration is Andre's call after your report.
- `docs/efficiency-implementation-status.md` is shared: workers do not edit it. Each worker
  writes `docs/efficiency/<task-name>.md`; you propose the status-doc update in your report.

## 2. Run setup

```
orca status --json
orca orchestration run-create --objective "Efficiency roadmap: outstanding items (waves 1-2)" --json
```

Create all six Tasks below with `task-create` before the first wait. Each Task spec is the
**task text below + the "Common worker rules" block appended verbatim**. Then:

- Wave 1 (T1, T2, T3): start immediately, one worker each:
  `orca orchestration worker-start --task <id> --worktree new-child --name <task-name> --base-branch feat/efficient-orchestration-memory --agent codex --model gpt-6-sol --json`
- Wave 2 (T4, T5, T6): attach one decision gate to each with `gate-create --question "Has the
  in-flight Codex session committed its work on feat/efficient-orchestration-memory? Wave 2
  touches those files." --options '["committed","hold"]'`. Start a wave-2 worker only after its
  gate resolves `committed`; base it on the branch head at that moment.

Check `launch.effective` in every `worker-start` receipt and report the model actually used.

## 3. Review and fix (one round per Task)

When an implementation worker sends `worker_done --outcome succeeded`:

1. Verify it yourself in that worktree: `git log --oneline -3`, `git diff --stat <base>..HEAD`,
   confirm it stayed inside its Ownership list, then run `make check` (and `make e2e` if it
   touched `internal/tui`, `internal/cli` or rendering). Record pass/fail with the tail of
   the output.
2. Start a **fresh** reviewer worker (new terminal, same worktree, `--worktree id:<that
   worktree id>`, `--agent codex --model gpt-6-sol`) with a review-only Task: "Adversarially
   review commits <base>..HEAD in this worktree against the original Task spec (pasted below).
   Do not edit files. Report findings as file:line, failure scenario, severity
   (blocker/major/minor), and whether a test proves it. Try to break the change: run the tests,
   write throwaway probes outside the repo if needed."
3. If there are blocker or major findings, reuse the **original implementer's terminal**
   (`worker-start --task <fix task> --terminal <handle> --worktree id:<worktree id>`) with a fix
   Task listing the findings verbatim. One fix round; anything left goes into the report.
4. Release each settled worker terminal per the guide. Retain one only if a failure needs
   inspection, and say so.

## 4. Final report (to Andre, in this terminal)

For each Task: outcome, worktree path and branch, commits, `make check`/`make e2e` result (with
evidence), review findings fixed and open, files touched, and anything blocked. Then a proposed
diff for `docs/efficiency-implementation-status.md` (moving delivered parts from
"Still outstanding" to "Implemented", keeping incomplete parts explicit). Finally, a suggested
merge order with expected conflicts. The turn ends only when `worker-list --run <run_id>
--terminal-state reclaimable --json` returns none.

---

## Tasks

### T1 `sandbox-shell` (wave 1): confined shell execution for isolated workers on macOS

- **Target:** `internal/sandbox/worker*.go`, `internal/execenv/confined_*.go`,
  `internal/agent/worker_isolation.go`, `internal/agent/shells.go`.
- **Change:** Wire the existing `sandbox.NewWorker` (`internal/sandbox/worker.go:27`, macOS
  Seatbelt profile, not yet connected to dispatch) into shell execution for isolated editing
  workers, behind an explicit opt-in. Today isolated workers are file-only. With the opt-in,
  their Bash commands run under the worker profile: writes only to the child checkout and its
  private temp dir, no parent-checkout or shared `.git` writes, no Unix sockets, no proxy or
  unsandboxed escape. Without the opt-in, behavior is unchanged.
- **Constraints:** Linux and Windows refuse shell activation with an explicit error (bubblewrap
  cannot block host Unix sockets; Windows has no confined file tools). Fail closed: if the
  profile cannot be built, the worker does not run the command. Reuse the per-worker process
  ownership in `internal/execenv/job_scope*.go`; commands still stop before file-scope cleanup.
- **Ownership:** may edit the Target files and add new files in `internal/sandbox`,
  `internal/execenv`, `internal/agent` (`worker_shell*.go`). Must not edit
  `internal/agent/dispatch.go` or `worker_batch*.go`; if wiring needs a change there, `ask` the
  coordinator with the exact hunk instead.
- **Observable acceptance:** darwin-tagged live tests that fail without the change: an
  isolated-worker shell command cannot write the parent checkout, cannot write shared Git
  metadata, cannot connect to a Unix socket, and can write inside its child checkout. A test that
  Linux/Windows activation errors. A test that the non-opt-in path is unchanged. `make check`
  passes. `docs/efficiency/sandbox-shell.md` states what is and is not contained.

### T2 `verify-discovery` (wave 1): broader Go verification dependency discovery

- **Target:** `internal/tools/task_verification.go`, `internal/tools/verification_status.go`.
- **Change:** Extend the opt-in `verificationDiscover: "go"` partial discovery. Today it
  snapshots only the selected package tree plus root module/workspace metadata. Add, statically
  and without running another command: (a) in-module packages transitively imported by the
  selected packages (parse imports with `go/parser` `ImportsOnly`); (b) local-path `replace`
  targets in `go.mod`; (c) `go test ./...` from the module root as one literal form. Keep the
  existing shared input limits; exceeding them yields no partial proof, as today.
- **Constraints:** Evidence stays labelled partial. Unchanged evidence never warrants skipping
  verification, and nothing suppresses the requested command. Still unsupported: shell
  expansions, flags, compound commands, build tags, cgo, toolchain and environment; record
  these as explicit unsupported reasons. Stay bounded on large trees.
- **Ownership:** may edit the Target files and add `internal/tools/verification_*.go` and tests.
  Must not edit `internal/cli/learning_outcomes.go`, `capability_search*.go` or `skill*.go`.
- **Observable acceptance:** tests that fail without the change: editing a file in an imported
  in-module package flips verification status to rerun-suggested; editing an unrelated package
  does not; a local `replace` target change is detected; the limit-exceeded path saves no
  partial proof. `make check` passes. `docs/efficiency/verify-discovery.md`.

### T3 `progress-loop` (wave 1): progress-loop detection beyond repeated failures

- **Target:** `internal/harness/turn.go` (current repeated identical-error and failed-cycle
  guards).
- **Change:** Add an opt-in guard for no-progress loops that are not failures: the same
  successful read-only call on unchanged content repeated past a threshold, edit/revert cycles
  that restore an earlier file state, and a fixed tool-call sequence repeated with no file or
  task-state change in between. On trigger, inject one bounded corrective notice (the same
  mechanism the existing guards use), then escalate per the existing tool-failure escalation if
  the loop continues.
- **Constraints:** No extra model calls. Bounded memory (fixed-size window of fingerprints).
  Forwarded to nested workers as the existing guards are; if that needs
  `internal/agent/dispatch.go`, `ask` the coordinator. The setting defaults off and repository
  settings cannot enable it if the user opted out (match how existing guard settings behave).
- **Ownership:** `internal/harness/turn.go`, new `internal/harness/progress_loop*.go`, and a new
  additive file `internal/claude/settings/progress_loop.go` (+ test) if a setting is needed.
  Must not edit the in-flight settings files listed in the brief.
- **Observable acceptance:** tests that fail without the change for each of the three loop
  shapes; a test that legitimate repeated reads after a file change do not trigger; a test that
  it is off by default. `make check` passes. `docs/efficiency/progress-loop.md`.

### T4 `context-reasons` (wave 2, gated): selection reasons in `/context` and wider provenance

- **Target:** `internal/tui/context.go`, `internal/tools/capability_search.go`, efficiency
  provenance reporting in `internal/claude/settings`.
- **Change:** `/context` shows, for each skill, agent, MCP tool and memory entry in or out of
  the request, why: pinned, excluded, suggested by metadata match, search hit, budget-dropped,
  or exact-name invocation. Extend per-field provenance (default, explicit override, evaluated
  policy) beyond the efficiency controls to the other settings `/context` reports.
- **Constraints:** Follow `docs/kiln-design-handoff/Terminal.dc.html` for anything on screen;
  new colours go into `internal/tui/theme.go` with the design's hex and name. Goldens: update
  only the tests you changed (`UPDATE=1 go test ... -run '<TestName>'`) and account for every
  changed row.
- **Ownership:** Target files, new files beside them, their tests and goldens.
- **Observable acceptance:** unit tests per reason; golden for the `/context` view; `make check`
  and `make e2e` pass. Note in your report that real-terminal verification (`kiln-qa`) is still
  needed and was not done by you. `docs/efficiency/context-reasons.md`.

### T5 `fact-extraction` (wave 2, gated): verified automatic fact extraction

- **Target:** `internal/learning/**`, `internal/memory/**`, `internal/cli/learning*.go`.
- **Change:** From ordinary task outcomes already captured (opt-in run aggregates), derive
  candidate project-memory facts (e.g. "tests for package X need build tag Y", "command Z is the
  verification command"). A candidate becomes a memory entry only after mechanical verification
  against the evidence it cites (command evidence still fresh, file snapshot still matches).
  Unverified candidates stay reviewable and dismissible, never auto-applied.
- **Constraints:** No model calls and no prompt context added during collection. Repository
  settings cannot enable capture or undo a user opt-out (existing rule). Respect existing
  expiry, attribution and correction history in `internal/memory/store.go`. No background paid
  evaluation.
- **Ownership:** Target files and tests.
- **Observable acceptance:** tests: a candidate whose evidence went stale is rejected; a verified
  one is stored with attribution; opt-out blocks capture; repo settings cannot enable it.
  `make check` passes. `docs/efficiency/fact-extraction.md`.

### T6 `patch-integration` (wave 2, gated): automatic integration of concurrent worker patches

- **Target:** `internal/agent/worker_batch.go`, `internal/agent/worker_snapshot.go`.
- **Change:** Opt-in automatic application of a completed batch of isolated-worker patch
  artifacts: when artifacts are disjoint and share the captured baseline, apply them under the
  repository lock using the existing batch preflight/apply and shared patch validation. When
  they overlap or the baseline moved, apply nothing and report the conflict set for explicit
  resolution.
- **Constraints:** All-or-nothing per batch. Reuse the existing digest/base checks; do not add a
  second validation path. Default stays explicit application.
- **Ownership:** Target files, new `internal/agent/worker_integrate*.go`, and tests.
- **Observable acceptance:** tests: disjoint batch auto-applies; overlapping batch applies
  nothing and lists conflicts; moved baseline applies nothing; default off. `make check`
  passes. `docs/efficiency/patch-integration.md`.

---

## Common worker rules (append verbatim to every Task spec)

> Repo `github.com/andrepato/harness` (kiln). Read `AGENTS.md` and
> `docs/efficiency-implementation-status.md` first. You are in your own child worktree; edit
> only the files in your Ownership list, and use the orchestration `ask` command before
> touching anything else. Every behavior change ships with a regression test that fails
> without it; show that it fails (revert, run, restore) in your report. Run `make check` before
> committing, and `make e2e` too if you touched `internal/tui`, `internal/cli` or rendering.
> Commit on your worktree branch with a clear message; do not push, open a PR, or merge. Do not
> edit `docs/efficiency-implementation-status.md`; write `docs/efficiency/<task-name>.md`
> instead (what changed, what is still not covered, evidence). Never run paid evaluations
> (`make eval-live`). Never write to the real `~/.claude`, `~/.claude.json` or `~/.harness`.
> Your `worker_done` summary names: commits, test names that fail without the change, the
> `make check` result, and remaining gaps. Use `--outcome failed` if acceptance is not met.
