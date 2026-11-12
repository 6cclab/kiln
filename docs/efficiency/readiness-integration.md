# Integrated readiness candidate

This report tracks `/Users/andrepato/orca/workspaces/harness-kiln/readiness-candidate`,
branch `anpato/readiness-candidate`, based on `02ffe46`. The original checkout and
`docs/efficiency-implementation-status.md` are untouched. All required,
independently approved sets are assembled for the integrated gates.

The preserved snapshot consists of `966fb18` (in-flight implementation),
`ceab8c0` (provenance report), and `02ffe46` (discovery and learning review
repairs). Their history remains behind the candidate branch; see
`readiness-candidate-snapshot.md` and `readiness-snapshot-fixes.md` for exact
preservation and red/green evidence.

## Final integration review repair

The independent review of integrated commit `90a0dcb` found that the
progress-loop branch in `internal/harness/turn.go` returned or continued after
a completed tool batch without calling `completeToolBatch`. A successful
retained worker patch followed by repeated successful reads could therefore
reach a corrective notice, then finish the operation, with no durable
integration decision. Continued repetition could reach the escalation error
with the same omission. The reviewer reproduced the first case with the
temporary overlay `/private/tmp/kiln_review_overlay.json` and test source
`/private/tmp/kiln_review_probe_test.go`.

This repair calls `completeToolBatch` after the remaining tool calls have
truthful skipped results and before either the progress-loop notice or error
exit. If a skipped-result commit or completion callback fails, the operation
stays pending so recovery can pair the result and resolve any durable intent;
the patch is never applied twice by this path. The other exits were audited:
ordinary batch completion and the older progress-limit failure already call
the callback; tool execution or skipped-result commit failures leave a
partially durable batch pending when a callback is installed. The completion
callback still receives the full batch only after all result entries are
durable. This change does not weaken the existing uncertain recovery rule.

`TestWorkerBatchProgressLoopDecisionBeforeNoticeOrEscalation` covers a worker
task followed by successful repeated reads, a later call skipped with a
truthful error result, and a subsequent repeated batch that escalates. It
checks one decision per complete batch, the durable decision and corrective
notice, terminal status, and that the skipped write did not execute.
`TestWorkerBatchProgressLoopCallbackFailureRemainsRecoverable` checks that
an error from the decision callback retains the pending operation. Both tests
failed against unmodified `90a0dcb` source with callback count zero or lost
pending state (`/private/tmp/kiln-readiness-review-progress-red.log`), then
passed after the fix (`/private/tmp/kiln-readiness-review-progress-green.log`).
The focused race run covering these and the existing worker integration tests
passed (`/private/tmp/kiln-readiness-review-progress-race.log`).
The coordinator's original overlay probes
`TestReviewProgressGuardCompletesWorkerBatch` and
`TestReviewWorkerTaskBatchStoppedByProgressGuard` also passed with this fix
(`/private/tmp/kiln-integration-fix-review-probes.log`).

This review round's required gates passed with a scratch HOME:

```sh
HOME=/private/tmp/kiln-readiness-review-home GOPATH=/Users/andrepato/go GOCACHE=/private/tmp/kiln-readiness-gocache make check
HOME=/private/tmp/kiln-readiness-review-home GOPATH=/Users/andrepato/go GOCACHE=/private/tmp/kiln-readiness-gocache make e2e
HOME=/private/tmp/kiln-readiness-review-home GOPATH=/Users/andrepato/go GOCACHE=/private/tmp/kiln-readiness-gocache go test -race -short ./internal/harness -run 'TestWorkerBatchProgressLoop|TestWorkerIntegration' -count=1
```

Logs are `/private/tmp/kiln-readiness-review-fix-check.log`,
`/private/tmp/kiln-readiness-review-fix-e2e.log` (223.070 seconds), and
`/private/tmp/kiln-readiness-review-progress-race.log`. No paid evaluation
or installed GOBIN replacement was used.

## Approved inputs applied so far

| Area | Original commits, in application order | Result |
| --- | --- | --- |
| Crash recovery | `51d009e`, `4f13da6` | Applied with `git cherry-pick --no-commit` |
| Distribution | `b711eae`, `c538d07` | Applied with `git cherry-pick --no-commit` |
| Partial verification discovery | `f0ccb76`, `316fdbe`, `7f64af1`, `0288c32` | Applied with `git cherry-pick --no-commit` |
| Context selection reasons | `ded22b2`, `d6d0a5c` | Applied with `git cherry-pick --no-commit` |
| Worker patch batch | `be8d7be`, `9e52a80` | Applied with `git cherry-pick --no-commit` |
| Shell and progress wiring | `858abd5`, `57cb696`, `96abf69`, `faf6bc2` | Applied together with `--no-commit`; unsafe `96abf69` was never a candidate endpoint |
| Verified facts | `8effd97`, `7cf946f` | Applied with `git cherry-pick --no-commit` |

All source worktrees below live under
`/Users/andrepato/orca/workspaces/harness-kiln/`; each source branch is
`anpato/<worktree>`. The roadmap table immediately below identifies the main
files and per-task limits, while the linked reports hold each task's detailed
red/green tests and review repairs.

| Task | Source worktree and branch | Source and approved integration commits | Detailed report |
| --- | --- | --- | --- |
| Snapshot | `readiness-candidate`, `anpato/readiness-candidate` | `966fb18`, `ceab8c0`, `02ffe46` | `readiness-candidate-snapshot.md`, `readiness-snapshot-fixes.md` |
| Runtime | `readiness-runtime-audit`, `anpato/readiness-runtime-audit` | `51d009e`, `4f13da6` | `readiness-runtime-fixes.md` |
| Distribution | `readiness-distribution-audit`, `anpato/readiness-distribution-audit` | `b711eae`, `c538d07` | `readiness-distribution-fixes.md` |
| T1 shell foundation and safety wiring | `sandbox-shell`, `anpato/sandbox-shell`; `readiness-wiring`, `anpato/readiness-wiring` | foundation `61460f0`; integrated `858abd5`, `96abf69`, `faf6bc2` | `sandbox-shell.md`, `readiness-wiring.md` |
| T2 verification discovery | `verify-discovery`, `anpato/verify-discovery` | `f0ccb76`, `316fdbe`, `7f64af1`, `0288c32` | `verify-discovery.md` |
| T3 progress loop and nested wiring | `progress-loop`, `anpato/progress-loop`; `readiness-wiring`, `anpato/readiness-wiring` | foundation `93c0810`; integrated `57cb696`, `96abf69`, `faf6bc2` | `progress-loop.md`, `readiness-wiring.md` |
| T4 context reasons | `context-reasons`, `anpato/context-reasons` | `ded22b2`, `d6d0a5c` | `context-reasons.md` |
| T5 verified facts | `fact-extraction`, `anpato/fact-extraction` | `8effd97`, `7cf946f` | `fact-extraction.md` |
| T6 worker batch | `patch-integration`, `anpato/patch-integration` | `be8d7be`, `9e52a80` | `patch-integration.md` |

## Roadmap coverage and ownership

| Track | Integrated outcome or pending decision | Main owned files |
| --- | --- | --- |
| T1 isolated worker confinement | Fail-closed safety fix applied: `isolatedShell` refuses on macOS, Linux and Windows before starting a child. File-only isolated workers remain available on macOS/Linux. General shell activation is still incomplete. | `internal/agent/dispatch.go`, `worker_isolation.go`, `worker_shell.go`, `internal/sandbox/worker.go`, focused tests and docs |
| T2 verification discovery | Applied partial, bounded Go import and local replacement discovery; cross-volume ancestors fail closed. | `internal/tools/task_verification.go`, `verification_go_dependencies.go`, `verification_status.go` and focused tests |
| T3 progress loop | Opt-in `progressLoopLimit` detects repeated successful reads and bounded read-only Bash sequences; one notice precedes escalation. Unknown state is excluded and user opt-out wins. | `internal/harness/progress_loop.go`, `turn.go`, settings, nested-worker wiring and focused tests |
| T4 context reasons | Applied current-request inclusion and settings provenance in `/context` and `kiln config`. | `internal/cli/context_selection.go`, `context_settings.go`, `internal/tui/context.go` and focused tests/goldens |
| T5 verified facts | Opted-in successful literal local Go test evidence yields bounded, reviewable candidates; eligible file-backed procedure records require current Read permission and fresh sources. Memory/learning opt-outs win. | `internal/learning/facts.go`, `internal/cli/learning_outcomes.go`, `internal/memory`, focused tests and docs |
| T6 worker patch batch | Applied opt-in, full-batch integration with durable outcome and uncertain recovery. | `internal/agent/worker_integrate.go`, `internal/harness` lifecycle, `internal/cli`/TUI presentation and focused tests |

The extra runtime fixes cover crash recovery and corrupt tool-history blocking
(`internal/harness`, `internal/cli`, PTY regressions); distribution fixes cover
release CI, installer, doctor and first-run guidance (`.github/workflows`,
`scripts/install.sh`, `README.md`, `docs/usage.md`, tests). The separate
snapshot source and reports are retained. Native Windows CI, release publishing,
the macOS general shell confinement gap, and broader live quality comparisons
remain open review findings. The completed review repairs and red/green probes
are documented in the source-area reports linked below.

The initial, T6, and T5 sets had no manual source conflicts; Git auto-merged
`internal/cli/chat.go`, `internal/harness/turn.go`, and settings files. The
last wiring safety commit conflicted with earlier wiring changes in
`docs/configuration.md`, `docs/efficiency/progress-loop.md`,
`readiness-wiring.md`, `sandbox-shell.md`, `internal/agent/dispatch.go`,
`dispatch_test.go`, and `worker_shell_darwin_test.go`. The wiring-only files
were resolved to the exact approved `faf6bc2` versions; the configuration
file took that version plus the local T6 documentation below. The resolved
wiring-only files compare byte-identical to `faf6bc2`. An audit against all
`96abf69` paths found that Git's automatic merge had dropped the
`isolatedShell` settings merge, provenance, config reporting and CLI dispatch
initializer, and the nested `progressLoopLimit` initializer and reporting.
Those additive hunks were restored without removing the T4/T5/T6 additions;
`autoIntegrate` and `cycleLimit` remain wired. The new real-`Run` dispatch
regressions `TestRunDispatchHonorsIsolatedShellSetting` and
`TestRunDispatchHonorsProgressLoopSetting` pass with the integrated source.
Deleting each respective initializer via temporary Go overlays makes its
test fail, proving both would catch the lost hunk. The overlay files are
`/private/tmp/kiln-readiness-red-cli-shell/overlay.json` and
`/private/tmp/kiln-readiness-red-cli-loop/overlay.json`. The separate runtime
prerequisites `142c7dc` and `4f5db25` were not duplicated because the
coordinator verified them equivalent to `51d009e` and `4f13da6`.

## Cross-feature checks in progress

- Recovery and batch lifecycle: the crash seed in
  `testdata/faux/qa/recovery-uncertain-seed.sh` starts a local faux model under
  a scratch HOME, waits for the shell marker and persisted tool arguments,
  kills kiln, and releases the tool. The new
  `qa/scenarios/startup/recovery-uncertain.steps` resumes that session,
  checks the truthful uncertain outcome and unchanged marker, and submits a
  fresh request. The script ran successfully with a scratch HOME; QA lint
  accepted the scenario. A visible terminal drive is reserved for the
  coordinator's final binary.
- `TestReadinessRecoveredBatchCompletesBeforeIntegration` reopens a partial
  two-tool operation with its first result committed. It verifies only the
  second tool runs, the integration callback sees both paired results after
  recovery, the notice is committed once, and the model receives it in the
  next request. With the recovered-batch callback disabled using a temporary
  Go overlay, the test failed with `completion calls=0 executions=1 results=2`;
  restoring production source passed. No repository source was reverted for
  this red probe.
- `qa/scenarios/tools/worker-integration-durable.steps` drives an opted-in
  two-worker batch from `testdata/faux/qa/worker-integration-durable.yaml` and
  checks the visible notice, both checkout files and the persisted session
  note. QA lint accepted this scenario. A non-visual scratch-HOME run of this
  exact faux fixture and the rebuilt worktree binary passed, including both
  files and the JSONL notice (`/private/tmp/kiln-durable-qa-4sntc1pb`); the
  real-terminal drive is pending.
- The focused tests in `internal/harness`, `internal/cli`, `internal/agent`,
  `internal/tools`, and `internal/tui` passed after the T6 integration. These include
  multi-tool recovery, later-prompt corruption blocking, current-request
  selection after compaction, memory permission, source precedence, and
  bounded Go discovery. The first sandboxed test attempt failed because local
  loopback binds were denied; the retry with loopback access passed.
- `GOCACHE=/private/tmp/kiln-readiness-gocache go test -tags e2e ./test/e2e
  -run 'TestTUI_Continue_AfterKilledPrintRun|TestSession_CrashMidToolThenResume'
  -count=1 -timeout 3m` passed against the combined runtime and T6 code.
- The independent final-branch e2e attempts reported `TestHooks_Timeout_KillsSleepForever`
  and `TestTools_BashTimeout` failing on global `pgrep -f` markers while the
  wiring and fact worktrees ran concurrently. Their logs are
  `/private/tmp/kiln-wiring-final-coordinator-e2e.log` and
  `/private/tmp/kiln-fact-final-coordinator-e2e.log`; this alone did not prove
  a product cleanup leak. The two tests now record their own spawned PID and
  unique command marker, then check that exact process after timeout. An
  unrelated owned control with the old global marker reproduced both old
  false positives: the focused red run failed on PIDs 1851 and 1920. The
  focused green run passed both tests with those controls active.
  `TestTimedOutProcessAssertionDetectsUncleanedOwnedProcess` leaves a separate
  owned process running until the new checker reports `still running`, then
  kills it and checks that the assertion clears. Every control has cleanup
  registered on test exit. These changes touch only
  `test/e2e/hooks_test.go` and `test/e2e/tools_behaviour_test.go` under the
  coordinator's narrow ownership extension. The final helper hashes the full
  random scratch path to derive a unique marker, and treats a `ps` lookup
  error or empty successful response as unknown, not as a clean exit.
  `TestTimedOutProcessAssertionFailsOnUnknownLookup` verifies that boundary.
- The complete-set focused packages (`internal/harness`, `internal/cli`,
  `internal/agent`, `internal/learning`, `internal/memory`, `internal/tools`,
  `internal/claude/settings`, `internal/tui`) passed. Targeted checks of T1
  shell refusal and final Bash permission, T3 read-only progress, T5 user
  opt-out/current Read permission, and T6 recovered-batch completion passed.
- Full `make check` passed after the additive wiring restoration in
  `/private/tmp/kiln-readiness-final-check-rerun.log` (earlier pre-restoration
  pass: `/private/tmp/kiln-readiness-final-check.log`). The initial full e2e
  run failed only at `TestTUI_Design_Context`: the Tools estimate rose from
  `3.9k` to `4.0k` after the integrated tool changes. The named-only update
  changed exactly that row in the plain and styled goldens; the full e2e
  rerun passed in `/private/tmp/kiln-readiness-final-e2e-rerun.log` (first
  failure: `/private/tmp/kiln-readiness-final-e2e.log`). `make race` passed in
  `/private/tmp/kiln-readiness-final-race.log`; `make eval INSTALL_DIR=` passed
  against the faux baseline in `/private/tmp/kiln-readiness-final-eval.log`.
  QA lint passed in `/private/tmp/kiln-readiness-final-qa-lint.log`.
  All gates used `HOME=/private/tmp/kiln-readiness-home` and
  `GOCACHE=/private/tmp/kiln-readiness-gocache`; the Go gates also set
  `GOPATH=/Users/andrepato/go`. Eval's empty `INSTALL_DIR` prevented a real
  installed binary replacement. Native Windows CI, release assets,
  paid/live comparisons, and general shell workers remain outside this local
  validation.

## Regression evidence

The source-area reports preserve the red/green probes on their own pre-fix
branches: `readiness-runtime-fixes.md`, `readiness-distribution-fixes.md`,
`verify-discovery.md`, and `context-reasons.md`. The integrated recovery-batch
and CLI dispatch probes above additionally fail when their relevant hunk is
removed. The e2e timeout assertion negative controls prove both that an
unrelated similarly named process no longer causes a false failure and that
an owned process left alive is still detected.

Independent checks reported by the coordinator passed for the snapshot
`02ffe46`, runtime `4f13da6`, distribution `c538d07`, T2 `0288c32`, T4
`d6d0a5c`, and T6 `9e52a80`. T2 also passed a Windows cross compile; native
Windows execution remains unavailable. The independently checked T5
`7cf946f` and wiring `faf6bc2` branches had e2e failures confined to the
two old global-marker timeout assertions under concurrent worktree runs.
The original failure logs, respectively, are
`/private/tmp/kiln-fact-final-coordinator-e2e.log` and
`/private/tmp/kiln-wiring-final-coordinator-e2e.log`. The final integrated
e2e passes with the assertion fix.

The coordinator independently reran `make check` and `make e2e` on
`90a0dcb`; both passed, with the e2e run taking 228.005 seconds. Logs are
`/private/tmp/kiln-readiness-candidate-coordinator-check.log` and
`/private/tmp/kiln-readiness-candidate-coordinator-e2e.log`. This review
repair has its own gates below, since the prior passes do not cover it.

The exact final gate invocations were:

```sh
HOME=/private/tmp/kiln-readiness-home GOPATH=/Users/andrepato/go GOCACHE=/private/tmp/kiln-readiness-gocache make check
HOME=/private/tmp/kiln-readiness-home GOPATH=/Users/andrepato/go GOCACHE=/private/tmp/kiln-readiness-gocache make e2e
HOME=/private/tmp/kiln-readiness-home GOPATH=/Users/andrepato/go GOCACHE=/private/tmp/kiln-readiness-gocache make race
HOME=/private/tmp/kiln-readiness-home GOPATH=/Users/andrepato/go GOCACHE=/private/tmp/kiln-readiness-gocache make eval INSTALL_DIR=
HOME=/private/tmp/kiln-readiness-home make qa-lint
```

## Coordinator terminal validation

The coordinator's iTerm2 and Terminal.app attempts both failed before kiln
launched: AppleEvent create-window timed out with `-1712`, and teardown then
reported a `None` tty error. The desktop was unlocked. No scenario passed and
there is no screenshot to review. This is an environment/driver blocker, not
evidence that kiln's visible terminal behavior passed or failed. The planned
target invocation remains:

```sh
python3 scripts/qa/drive.py --terminal iterm-dark \
  qa/scenarios/commands/context.steps \
  qa/scenarios/commands/context-reasons.steps \
  qa/scenarios/tools/edit-existing.steps \
  qa/scenarios/resize/fullscreen-toggle-permission-prompt.steps \
  qa/scenarios/startup/recovery-uncertain.steps \
  qa/scenarios/tools/worker-integration-durable.steps
```

The coordinator separately drove all six listed scenarios in headless
`xterm-dark` against `90a0dcb`: all passed, and the screenshots were inspected
for context selection, existing-file diff, fullscreen permission prompt,
uncertain recovery, and durable worker integration notice. Evidence is
`/private/tmp/kiln-readiness-headless-90a0dcb.log` and the result files under
`/private/tmp/kiln-readiness-headless-90a0dcb/xterm-dark/`. This is useful
nonvisual coverage, not the blocked visible macOS terminal gate. The
coordinator will rerun affected scenarios on the repaired binary.

The coordinator also ran one zero-paid local loopback smoke on the exact
`90a0dcb` binary, SHA-256
`39b039b3ce74d9eb5e186c1ea68fdebf3789189d92bad663677396278a489aa9`.
Under an isolated HOME with `HARNESS_OFFLINE=1`, `ollama/bonsai-2:27b-q2_0`
repaired a four-case Node arithmetic fixture: baseline verification exited 1,
kiln exited 0, and post-edit verification exited 0 in 122.3 seconds. The
scratch evidence is `/private/tmp/kiln-local-smoke-yxqv87om` (`result.json`,
`stdout.json`, `stderr.log`, `served.json`, and `project/src/math.js`). This
run made seven turns and six tool calls with reported cost USD 0. Kiln's two
attempted Node Bash checks were correctly blocked in noninteractive
`acceptEdits` mode; the passing behavioral check was the coordinator's
independent Node VM verification after the edit. This one bounded coding task
is not a broad model-quality comparison. It ran
before this review repair; no model smoke has yet been repeated on the
repaired binary. This worker does not move focus or run visible drives.

## Proposed status document update

Do not apply this patch in this worktree. This is the proposed edit to
`docs/efficiency-implementation-status.md` after final validation; adjust the
T5 line if its independently reviewed behavior differs from the description
below.

```diff
@@ Implemented
-  Opt-in discovery for literal local Go package tests adds a bounded partial
-  package/metadata snapshot, including the absence of optional workspace files.
+  Opt-in discovery for literal local Go package tests adds a bounded partial
+  snapshot of transitive in-module imports, local replacement targets and
+  package metadata, including absent optional workspace files. Unsupported
+  source forms and cross-volume ancestors fail closed without skipping checks.
@@ Implemented
-  Batch preflight/application combines disjoint artifacts from one clean base.
+  Batch preflight/application combines disjoint artifacts from one shared base.
+  With a user-level opt-in, complete successful tool batches apply compatible
+  worker artifacts together; the decision is durable and uncertain crash
+  windows require inspection rather than automatic replay.
@@ Implemented
-  explicit overrides and evaluated policy sources.
+  explicit overrides and evaluated policy sources. `/context` shows observed
+  selection reasons for current-request material and effective setting sources.
@@ Implemented
-  identical-error and multi-call failed-cycle guards, all forwarded to nested workers.
+  identical-error and multi-call failed-cycle guards, all forwarded to nested workers.
+  An opt-in progress-loop guard also notices repeated successful reads and
+  bounded read-only Bash cycles before escalating continued no-progress runs.
@@ Implemented
+- Crash recovery pairs every tool call with a truthful result, preserves full
+  batches and refuses provider requests on corrupt tool history until repair.
+- Successful literal local Go test evidence can yield a reviewable fact
+  candidate; a narrowly eligible file-backed procedure is stored only with
+  fresh sources, current Read permission and user opt-in.
@@ Still outstanding from the full plan
-- General-purpose isolated workers with shell/sandbox support and automatic
-  integration of concurrent patches. Current isolated workers support file edits
-  from clean HEAD or an opt-in dirty snapshot and require explicit application.
-  A macOS worker-profile factory is implemented and tested independently but is
-  not connected to worker shell execution. Activation still needs integration
-  and validation of the combined confinement path; Linux needs protection against
-  arbitrary host Unix socket access. File tools now use descriptor-relative access.
+- General-purpose isolated shell workers remain unavailable. Activation fails
+  closed on macOS because detached descendants cannot be reliably owned, and
+  Linux still needs protection against arbitrary host Unix socket access.
+  Isolated file workers remain available; automatic patch integration requires
+  explicit user opt-in and does not make shell execution safe.
@@ Still outstanding from the full plan
- Automatic verification dependency discovery and broader selective test policies;
-  current evidence covers declared files/directories and opt-in partial discovery
-  for narrow Go package commands, not a full dependency graph, whole-task
-  correctness or proof that unchanged checks can be skipped.
+- Broader automatic verification dependency discovery and selective test policies;
+  current evidence includes opt-in bounded Go imports and local replacements,
+  not a full dependency graph, whole-task correctness or proof that unchanged
+  checks can be skipped.
@@ Still outstanding from the full plan
-- Verified automatic fact extraction and background policy-candidate generation
-  from ordinary task outcomes. Run statistics produce review hypotheses only;
-  policy candidates still require explicit evaluation evidence, and plans do not
-  schedule or execute paid evaluations.
+- Background policy-candidate generation from ordinary task outcomes. Run
+  statistics produce review hypotheses only; policy candidates still require
+  explicit evaluation evidence, and plans do not schedule or execute paid
+  evaluations.
@@ Still outstanding from the full plan
- Broader file/result reuse, progress-loop detection beyond repeated failure sequences,
-  selective verification policies, and reviewable skill/harness improvement proposals.
+- Broader file/result reuse and progress-loop detection beyond repeated reads
+  and bounded read-only Bash cycles, selective verification policies, and
+  reviewable skill/harness improvement proposals.
@@ Still outstanding from the full plan
-- Provenance outside the efficiency controls and detailed selection reasons in
-  `/context`; broader head-to-head evaluation with complete external usage attribution.
+- Provenance outside the efficiency controls and selection reasons beyond
+  observed current-request material; broader head-to-head evaluation with
+  complete external usage attribution.
```
