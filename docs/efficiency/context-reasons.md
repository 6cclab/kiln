# Context selection reasons

## Changes

`/context` reports why skill, agent, MCP, and memory material is in or out of
the current request. Successful skill and agent tool results and capability
search hits are reconstructed from tool-call IDs and results in the same
compaction-projected branch used for the next request. Restored sessions show
committed results; compacted-away results disappear. Search budget drops are
kept by committed call ID. Current-turn suggestions and personal-memory
retrieval decisions belong to the last typed request, and the next one replaces
them. All of these survive a restart (see "Persistence" below). The ledger keeps
at most 64 drop call IDs and displays at most 64 rows.

Catalog-mode skill metadata is checked against the active system prompt, and
configured exclusions are shown only for loaded, model-invocable skills.
Claude instruction files (`CLAUDE.md` and rules) and Claude auto-memory
`MEMORY.md` appear as system-prompt memory material with their own labels,
distinct from retrieved personal-memory records. Project and global
personal-memory retrieval still reports each search hit or budget drop at the
retrieval point for the current request.

The `/context` settings section and `kiln config` `contextSettings` report
the effective model, effort, permission mode, and accepted auto-memory
directory. Their sources reflect CLI flags, environment, resumed session,
accepted settings files, or defaults. The `configuredSettings` object in
`kiln config` holds static file configuration, including model roles and
enablement flags. These paths call the existing runtime resolvers
for model, effort, permission mode, workspace trust, and unsafe auto-memory
directory rejection.

## Persistence

Just before each model request is sent (an `OnBeforeRequest` hook, after the
context transforms ran), kiln commits the request's decisions to the session
value store as namespace `kiln.context.selection`, key `last`
(`internal/cli/context_selection.go`, written through `Lane.SetSessionValue`).
The record holds a SHA-256 digest of the typed request, the transient rows
(kind, material, name, reason, included) and the search-drop ledger by call ID,
capped at the existing 64 rows and 64 call IDs (10 rows per call). It holds
no prompt text and no memory content, only names, keys and reason codes. An
unchanged record is not rewritten.

On resume, `chat.go` reads the record into the ledger before the hooks are
registered, so `/context` and `kiln config` show it. Rows are shown only while
the digest matches the last typed request of the compaction-projected branch,
the same rule live rows use: switching to another branch or compacting the
request away hides them, and a new typed request replaces them. Sessions
without the record, or with an unknown version or malformed record, behave as
before.

## Limits

A search drop cannot occur after the final request of a turn: the result always
precedes another request, which commits it. A crash between a request
being sent and its commit loses only that request's decisions. Only the last
request is kept; earlier requests' transient decisions are not recoverable.
The displayed row limit can omit later alphabetically sorted entries in
unusually large sessions. Real-terminal `kiln-qa` verification of the final
integrated binary is still needed; this worker did not perform that drive.

## Evidence

- Live faux-backed requests: `TestContextSelectionLiveRestoreAndCompaction`
  checks the skill-body row and request bytes before and after compaction,
  across process restarts. `TestContextSelectionLiveCatalogAndClaudeMemory`
  checks catalog metadata and both kinds of Claude memory against actual
  system-prompt bytes. `TestContextSettingsLiveResumedModelSource` checks a
  restored model in both `/context` and `kiln config`.
- Projection tests: `TestContextSelectionProjectedCompactionAndRestore`
  checks skill, agent, and search results plus call-ID-bound drops;
  `TestContextSelectionCatalogAndClaudeMemoryCurrentPrompt` checks prompt
  inclusion; `TestContextSelectionExclusionRequiresLoadedSkill` rejects a
  nonexistent configured exclusion.
- Source tests: `TestEfficiencyConfigRuntimeOverridePrecedenceAndRejectedMemoryDir`
  checks CLI overrides and unsafe path fallback;
  `TestEfficiencyConfigEnvironmentAndUntrustedDirectoryPrecedence` checks
  environment precedence and an untrusted project override.
- Deterministic QA script: `qa/scenarios/commands/context-reasons.steps` and
  `testdata/faux/qa/commands-context-reasons.yaml`; `scripts/qa/drive.py
  --lint qa/scenarios/commands/context-reasons.steps` passed. The coordinator
  will drive the final binary in a real terminal.
- `TestRenderGolden_Context` updated only the named render golden: its added
  rows show the Settings section, effective model, effort, permission, and
  auto-memory directory with sources. `TestTUI_Design_Context` updated only
  the named PTY golden: it adds an agent metadata suggestion, the Settings
  heading, and four settings rows. Existing styled terminal padding is
  preserved.

- Restart persistence: `TestContextSelectionLiveRestartKeepsRequestDecisions`
  (faux-backed; a pinned suggestion, a memory hit and a search budget drop
  appear in `/context` and `kiln config` from a fresh process and a new typed
  request replaces them), `TestContextSelectionLiveOldSessionWithoutRecord`
  (record stripped from the session file) and
  `TestContextSelectionRecordHoldsNoContentAndIsBounded`. With the commit
  disabled both live tests fail (all three rows missing; no record written).

### Red/green proof

Temporary `go test -overlay` source changes under `/private/tmp` disabled one
fix at a time. Each named test failed without the behavior and passed with the
overlay removed; repository source was never changed by these probes:

| Removed behavior | Red regression |
| --- | --- |
| Skill result projection | `TestContextSelectionLiveRestoreAndCompaction` (restored row missing) |
| Current-request guard on transient suggestions | `TestContextSelectionProjectedCompactionAndRestore` (stale pin after compaction) |
| Catalog prompt inspection | `TestContextSelectionLiveCatalogAndClaudeMemory` (catalog row missing) |
| Loaded-skill exclusion filter | `TestContextSelectionExclusionRequiresLoadedSkill` (nonexistent `typo` row) |
| CLI model precedence | `TestEfficiencyConfigRuntimeOverridePrecedenceAndRejectedMemoryDir` (file model reported instead) |

### Gates

`make check` passed (gofmt, vet, staticcheck, unit tests, and e2e vet).
Full `make e2e` passed (`test/e2e`, 231.898s). The named render and PTY
goldens passed after their narrow updates. `git diff --check` passed, excluding the two styled goldens' intentional
terminal padding.
