# Readiness snapshot: review fixes

The preserved snapshot remains in commits `966fb18` and `ceab8c0`. This round
fixes review findings 1, 3, and 4 on `anpato/readiness-candidate`; it does not
alter the original `/Users/andrepato/projects/harness-kiln` checkout.

## Changes

| Finding | Fix | Regression |
|---|---|---|
| Custom commands absent from live discovery | `Run` merges personal, project, and enabled plugin commands into the effective skill records before exclusion filtering, search, and `skill` tool construction. The existing command precedence and `disable-model-invocation` rules apply to the live catalog. | `TestRun_CustomCommandDiscoveryAndExactInvocation` drives a scratch-home faux session through `capability_search` and exact `skill` invocation; search exposes metadata and invocation renders `$ARGUMENTS`. |
| Live learning budget recorded as zero | The observer receives `SkillDiscovery.Budget` for the session's resolved tier after effective settings and evaluated personal policy are applied. User learning opt-out and observation limits are unchanged. | `TestRun_LearningObserverRecordsEffectiveDiscoveryBudget` runs a scratch-home opt-in session and reads the stored outcome with budget 512. |
| Proposals stale after cohort or budget changes | Active proposals now refresh their text, evidence, FTS row, and timestamp in a bounded transaction. The same transaction checks explicit dismissal; unsupported proposals are withdrawn when retained outcomes no longer justify them. No proposal is evaluated or activated automatically. | `TestReadinessProposalRefreshesRetainedCohortAndBudget` verifies a retained 512-token cohort changes to 1024 tokens without changing the stable proposal ID, and that dismissal persists. `TestReadinessProposalWithdrawsWithoutSupportingCohort` verifies withdrawal when the retained high-input cohort disappears. |

## Regression proof

The four new tests were first run against the unfixed candidate and failed at
their asserted behavior. A second proof used Go's read-only `-overlay` to map
`internal/cli/chat.go`, `internal/learning/proposals.go`, and
`internal/memory/store.go` to their `ceab8c0` contents while leaving the new
tests in place. All four tests failed for the same reasons: command search had
no match, the observer stored budget 0, and proposals retained the old cohort
and survived loss of supporting evidence. Removing the overlay restored the
working code without resetting the worktree; all four tests passed.

| Evidence | Result | Log SHA-256 |
|---|---|---|
| `go test -overlay=/private/tmp/kiln-readiness-fixes-overlay/overlay.json ./internal/cli ./internal/learning -run 'TestRun_CustomCommandDiscoveryAndExactInvocation\|TestRun_LearningObserverRecordsEffectiveDiscoveryBudget\|TestReadinessProposalRefreshesRetainedCohortAndBudget\|TestReadinessProposalWithdrawsWithoutSupportingCohort' -count=1` | Expected failure in all four tests; `/private/tmp/kiln-readiness-fixes-reverted-overlay.log` | `73b7fc0494d51df0fd22bed0229621533f2896db38f68261e957706bef9ab607` |
| Same targeted command without `-overlay` | Passed; `/private/tmp/kiln-readiness-fixes-restored.log` | `4eccfc6075fde5698022451072b0d4df23508d92664704474e64af510e6ed863` |
| `go test ./internal/cli ./internal/learning ./internal/memory -count=1` | Passed, including existing precedence, exclusion, dismissal, and opt-out tests; `/private/tmp/kiln-readiness-fixes-packages.log` | `1d2cda6af3ff4c736acd338189f509a71106ad1e1a228f2a1afe6247a3fbb0d3` |
| `make check` | Passed: gofmt, vet, staticcheck 2026.2.2, unit tests, e2e-tag vet; `/private/tmp/kiln-readiness-fixes-check.log` | `518d5002ae778252ea5c101f24a58613387a24015ca70be5bc1f704ca6dc57cd` |
| `make e2e` | Passed: `test/e2e` in 245.805s; `/private/tmp/kiln-readiness-fixes-e2e.log` | `1c631396c3821609ff7681ef522bd5fc2093bbe3b8b35d1303440d5f825bd9a9` |

The gates used a scratch `GOCACHE`; no paid evaluations or real model calls
were made. The first `make check` attempt with `GOPROXY=off` could not resolve
the pinned staticcheck command, so the exact gate was rerun successfully with
network access.

## Remaining work

Review finding 2, automatic worker integration, is assigned to T6
(`task_dcb37c2eb808`). This round did not wire `autoIntegrate`, change its trust
policy, or add a batch call site. Plugin hooks and broader instruction types
also remain outside this fix round. No push, PR, or merge was made.
