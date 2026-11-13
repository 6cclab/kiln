# Verified fact extraction

Opted-in main-lane run capture now inspects successful Bash verification evidence already in tool results. A completed run can produce a reviewable candidate for a literal `go test ./...` or local-package command. The aggregate outcome remains sanitized; no prompt, command, or path is added to it, and extraction makes no model call or prompt-context addition.

The only automatically stored memory is a narrow procedure: **“Observed successful verification command: …”**. A successful command with a build tag does not establish that the tag is required. Promotion requires the effective current Read decision, an unchanged verification snapshot, and one to eight declared regular files at most 1 MiB each. Read permission is checked before each freshness read, around each source capture, and again before storage. Denied Read access, paths outside the project root, changed inputs, source files above 1 MiB, and oversized proof payloads remain review-only with explicit reasons. Commands with shell operators or unsupported shapes are skipped.

Candidates are kept for seven days in a separate project scope and can be listed with `kiln learning fact-candidates` and removed with `kiln learning dismiss-fact <id>`. The `stored` status records actual promotion and an active derived memory row; `review-only` carries the reason no fact was stored. Status records the promotion outcome and does not rehash sources during review. Verified memory entries have outcome and tool-call attribution, file sources, a 30-day expiry, and a stable key that will not overwrite a user correction. A repeated verified observation can refresh an expired derived entry while retaining its history. Source bytes and file modes are checked during retrieval. `forget-outcomes` clears review candidates and dismissals; stored memory facts remain subject to normal memory forgetting and expiry. Dismissing a candidate does not forget an already stored fact; use the normal memory forget command.

Automatic fact rows, including their automatic history, are capped at eight per command and 100 per project. Pruning removes the old row, search entry and source metadata together. An automatic ancestor of a user correction is exempt so correction history remains restorable; explicit records and corrections are never pruned, even when their text resembles an automatic fact. Version-1 automatic rows are recognized once during migration by their original marker and source-bearing shape. The run observer holds at most 16 proofs and 64 KiB of serialized proof data; when personal memory is opted out, it retains no raw proofs while aggregate counts continue.

Aggregate capture uses the existing user-controlled `learningAutomation.enabled` opt-in and `learningEnabled` opt-out. Automatic fact extraction also requires personal memory to be enabled; `personalMemoryEnabled:false` leaves opted-in aggregates intact but blocks candidate and memory writes. Repository settings cannot enable capture or undo either user opt-out. Capture records observed success, not command sufficiency, dependency completeness, or task correctness. No paid evaluation runs in the background.

## Subdirectory launches and directory dependencies

**Working directory.** The observer splits the launch directory into the worktree root (`memoryProjectRoot`, the Git work tree) and the slash-separated directory below it. The memory scope already resolves to the common Git dir, so every worktree of a project shares facts; sources are stored with project-root-relative paths and rebound to the retrieving worktree's root (`SourcesCurrentAt`). Verification proofs stay relative to the launch directory; promotion re-verifies them there (`tools.VerificationFreshContext`), maps each path to a root-relative path, and refuses any that leave the root, contain `.git`, or are absolute. Read-permission callbacks keep receiving launch-directory-relative paths, as they do from the verification tools. The working directory must be a plain relative name (ASCII letters, digits, `_ . / -`, at most 200 bytes), otherwise the candidate is review-only. The text records it: “Observed successful verification command (in internal/foo): go test ./...”. The key hashes the command, working directory and every dependency; a root launch with file dependencies only keeps its original key and text, so existing rows are unchanged.

**Source kinds.** `memory.Source` gained a `kind` field. The empty kind is a regular file, which is what every existing row holds, so no row migration is needed and old rows keep working. `dir` binds a directory's recursive membership: a SHA-256 over sorted `type, path, mode, content hash` entries (`.git` skipped, symlinks and special files refused, at most 512 entries and 16 MiB). It covers file contents and modes as well as names, because the verification proof it stands for does. The digest is rebuilt from the verification proof and must equal the captured one. `absent` is fresh only while `Lstat` still reports the path missing. A fact holds up to eight sources, files, directories and absent paths together. A binary that predates the field reads such a row as a file source and treats it as stale, so the fact is ignored rather than trusted.

**Retrieval.** `Current` and `Search` recheck every source kind, so adding, removing or editing a directory member, or creating an absent path, hides the fact until it is observed again.

Tests: `TestFactSubdirectoryLaunch*`, `TestFactDirectoryDependency*`, `TestFactAbsentDependency*`, `TestFactDependencyReadPermission*` (`internal/learning/facts_dependencies_test.go`), `TestDirectoryAndAbsentSourcesTrackMembership`, `TestCaptureSourceSpecsRefusesUnsafeShapes`, `TestLegacySourceRowsWithoutKindStillWork` (`internal/memory/sources_kinds_test.go`), `TestFactLaunchDirSplitsRootAndSubdirectory`, `TestOutcomeObserverPromotesSubdirectoryLaunch` (`internal/cli/learning_outcomes_workdir_test.go`). Repository settings still cannot enable capture: `TestRun_RepoLearningSettingsCannotEnableFactCapture`.

## Remaining limits

- Automatic promotion covers a single literal local Go test command, launched at the project root or in a subdirectory of it. Other commands and shell expressions are outside this extractor.
- Evidence paths that resolve outside the project root (for example a Go `replace` target above it), symlinked or special entries inside a directory, directories over 512 entries or 16 MiB, and member files over 1 MiB stay review-only.
- `stored` records promotion and database retention, not current source freshness; memory retrieval performs that check.
- Correction ancestors remain even if they bring the number of automatically originated rows above the nominal caps.
- Version-1 migration identifies old automatic rows by their original text, evidence, key, and source shape. A pre-migration explicit row that copied all of those fields cannot be distinguished mechanically; later explicit writes carry separate provenance.

## Evidence

The adversarial review at `8effd97` reproduced a >1 MiB source candidate claiming `verified` without a stored fact and 105 fact rows after 105 changed snapshots. Initial coordinator gates passed at `/private/tmp/kiln-fact-extraction-coordinator-check.log` and `/private/tmp/kiln-fact-extraction-coordinator-e2e.log` (225.848 s). Added regressions failed before implementation in `/private/tmp/kiln-fact-review-red-learning.log` and `/private/tmp/kiln-fact-review-red-observer.log`. Temporary-disable failures are in `/private/tmp/kiln-fact-review-red-permission.log`, `/private/tmp/kiln-fact-review-red-retention.log`, `/private/tmp/kiln-fact-review-red-bound.log`, `/private/tmp/kiln-fact-review-red-large-reason.log`, and `/private/tmp/kiln-fact-review-red-correction.log`. The focused green run is `/private/tmp/kiln-fact-review-green-focused.log`.

Each red, run, restore command group below mutates one file temporarily, then restores its exact bytes. Run them from this worktree with no concurrent editor or test process touching that file.

```sh
cp internal/learning/facts.go /private/tmp/kiln-fact-review-facts-backup.go
python3 -c 'from pathlib import Path; p=Path("internal/learning/facts.go"); s=p.read_text(); old="func factSourcesForPromotion(ctx context.Context, root string, proof tools.VerificationEvidence, allowRead func(string) bool) ([]memory.Source, string) {"; assert old in s; p.write_text(s.replace(old, old+"\n\tallowRead = func(string) bool { return true }", 1))'
GOCACHE=/private/tmp/kiln-fact-review-cache go test ./internal/learning -run '^TestFactReadPermissionRevoked' -count=1 -v > /private/tmp/kiln-fact-review-red-permission.log 2>&1
cp /private/tmp/kiln-fact-review-facts-backup.go internal/learning/facts.go

cp internal/memory/store.go /private/tmp/kiln-fact-review-store-retention-backup.go
python3 -c 'from pathlib import Path; p=Path("internal/memory/store.go"); s=p.read_text(); old="if err := pruneDerivedFacts(ctx, tx, r.Scope); err != nil {"; assert old in s; p.write_text(s.replace(old, "if err := (error)(nil); err != nil {", 1))'
GOCACHE=/private/tmp/kiln-fact-review-cache go test ./internal/learning -run '^TestFactRetentionBounds' -count=1 -v > /private/tmp/kiln-fact-review-red-retention.log 2>&1
cp /private/tmp/kiln-fact-review-store-retention-backup.go internal/memory/store.go

cp internal/cli/learning_outcomes.go /private/tmp/kiln-fact-review-observer-backup.go
python3 -c 'from pathlib import Path; p=Path("internal/cli/learning_outcomes.go"); s=p.read_text(); assert "maxCollectedFactProofs = 16" in s and "maxCollectedFactProofBytes = 64 << 10" in s; s=s.replace("maxCollectedFactProofs = 16", "maxCollectedFactProofs = 1000", 1).replace("maxCollectedFactProofBytes = 64 << 10", "maxCollectedFactProofBytes = 1 << 20", 1); p.write_text(s)'
GOCACHE=/private/tmp/kiln-fact-review-cache go test ./internal/cli -run '^TestOutcomeObserverBoundsProofCollectionAndInvalidatesFailedCommand$' -count=1 -v > /private/tmp/kiln-fact-review-red-bound.log 2>&1
cp /private/tmp/kiln-fact-review-observer-backup.go internal/cli/learning_outcomes.go

cp internal/learning/facts.go /private/tmp/kiln-fact-review-large-backup.go
python3 -c 'from pathlib import Path; p=Path("internal/learning/facts.go"); s=p.read_text(); old="if info.Size() > 1<<20 {"; assert old in s; p.write_text(s.replace(old, "if false && info.Size() > 1<<20 {", 1))'
GOCACHE=/private/tmp/kiln-fact-review-cache go test ./internal/learning -run '^TestFactLargeSourceIsReviewOnlyWithReason$' -count=1 -v > /private/tmp/kiln-fact-review-red-large-reason.log 2>&1
cp /private/tmp/kiln-fact-review-large-backup.go internal/learning/facts.go

cp internal/memory/store.go /private/tmp/kiln-fact-review-store-backup.go
python3 -c 'from pathlib import Path; p=Path("internal/memory/store.go"); s=p.read_text(); old="expiry == 0 || expiry > time.Now().Unix() || !oldAutomatic"; assert old in s; p.write_text(s.replace(old, "expiry == 0 || expiry > time.Now().Unix()", 1))'
GOCACHE=/private/tmp/kiln-fact-review-cache go test ./internal/learning -run '^TestFactDoesNotRefreshExpiredManualCorrectionWithDerivedText$' -count=1 -v > /private/tmp/kiln-fact-review-red-correction.log 2>&1
cp /private/tmp/kiln-fact-review-store-backup.go internal/memory/store.go

GOCACHE=/private/tmp/kiln-fact-review-cache go test ./internal/learning ./internal/memory ./internal/cli -run 'TestFact|TestOutcomeObserver|TestAutomaticFact|TestRun_PersonalMemoryOptOut|TestRun_RepoLearningSettings|TestLearningFactCandidates' -count=1 -v > /private/tmp/kiln-fact-review-green-focused.log 2>&1
```

`TestFactLargeSourceIsReviewOnlyWithReason` fails if its 1 MiB eligibility branch is disabled; `TestFactDoesNotRefreshExpiredManualCorrectionWithDerivedText` fails if the explicit-provenance check is disabled. `TestOutcomeObserverUsesLiveGateReadRuleAtRunEnd` covers a live Read rule added between the tool result and run end. Final `GOCACHE=/private/tmp/kiln-fact-review-cache make check` passed, with output at `/private/tmp/kiln-fact-review-check.log`. Final `GOCACHE=/private/tmp/kiln-fact-review-cache make e2e` passed in 230.711 seconds, with output at `/private/tmp/kiln-fact-review-e2e.log`.
