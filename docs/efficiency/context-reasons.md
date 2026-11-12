# Context selection reasons

## Changes

`/context` reports why skill, agent, MCP, and memory material is in or out of
the current request. Successful skill and agent tool results and capability
search hits are reconstructed from tool-call IDs and results in the same
compaction-projected branch used for the next request. Restored sessions show
committed results; compacted-away results disappear. Search budget drops are
kept by committed call ID, while current-turn suggestions and personal-memory
retrieval decisions are transient and cleared for a new typed request. The
ledger retains at most 64 drop call IDs and displays at most 64 rows.

Catalog-mode skill metadata is checked against the active system prompt, and
configured exclusions are shown only for loaded, model-invocable skills.
Claude instruction files (`CLAUDE.md` and rules) and Claude auto-memory
`MEMORY.md` appear as system-prompt memory material with their own labels;
they are distinct from retrieved personal-memory records. Project and global
personal-memory retrieval still reports each search hit or budget drop at the
retrieval point for the current request.

The `/context` settings section and `kiln config` `contextSettings` report
the effective model, effort, permission mode, and accepted auto-memory
directory. Their sources reflect CLI flags, environment, resumed session,
accepted settings files, or defaults. The `configuredSettings` object in
`kiln config` explicitly contains static file configuration, including model
roles and enablement flags. These paths call the existing runtime resolvers
for model, effort, permission mode, workspace trust, and unsafe auto-memory
directory rejection.

## Limits

Current-turn metadata suggestions and personal-memory retrieval are not
persisted in a session. After a process restart `/context` can recover
committed tool content and the current system prompt, but it cannot recover
those unstored decisions for a prior request. Once a new user request starts,
the view reports that request's transient decisions. Search budget drops
likewise cannot be recovered after restart because the dropped candidates
were never written to the transcript. The displayed row limit can omit later
alphabetically sorted entries in unusually large sessions. Real-terminal
`kiln-qa` verification of the final integrated binary is still needed; this
worker did not perform that drive.

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
goldens passed after their narrow updates. `git diff --check`, excluding
the two styled goldens' intentional terminal padding, passed.
