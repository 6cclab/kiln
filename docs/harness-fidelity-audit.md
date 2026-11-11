# Harness fidelity audit — 2026-11-11

This first offline audit targets defects that can make a model less effective:
missing instructions, damaged provider state, and blocked memory retrieval.
It adds no mandatory model review stage, changes no quality/cost policy, and
uses no paid model runs. The frozen memory-import benchmark artifacts remain
unchanged.

## Confirmed defects fixed

| Defect | Effect | Regression evidence |
|---|---|---|
| Only direct `.claude/rules/*.md` children were discovered | Nested user/project instructions never reached assembled context | `TestLoadMemoryNestedRules` failed with missing instructions, then passed with recursive discovery |
| Anthropic `redacted_thinking` was parsed as ordinary thinking | Opaque provider data disappeared before the next tool-result request | `TestAnthropicRedactedThinkingRoundTrip` failed before the fix; now checks streaming, JSON persistence, replay, display privacy, and subsequent tool-call indexing |
| Memory source hashing opened a path before checking its type | A FIFO could block capture and both source-freshness paths | All three child-process probes in `TestMemorySourcesRejectFIFO` hit their deadline before the fix; all return promptly after it |

Rule discovery preserves deterministic ordering, root-level rules, and a
symlinked rules root. Review caught a root-symlink regression in the initial
implementation; its new compatibility test passes both the baseline loader and
the final fix. Child symlink directories remain untraversed, so a directory
cycle cannot trigger recursive discovery.

Redacted provider blocks use the existing opaque `ProviderBlock` representation
and remain out of text/thinking display events. Memory hashing checks file type
before opening and again on the open descriptor; on Unix it opens nonblocking
so replacement with a FIFO between those checks cannot block the open.

## What the existing benchmark traces establish

Both Kiln attempts received the complete `sourceDigest` implementation. Their
recorded sessions contain no compaction entries, and none of their 107 total
tool results records output truncation. Each attempt had two edit errors and
subsequently recovered. These observations do not establish that context
reduction caused the benchmark defects.

Several test commands piped output through `tail` or `grep`. Under ordinary
shell semantics, the pipeline can exit successfully despite a failed test.
The failing test output was still present, and the model investigated and
reported those failures. No file-bound verification proofs were recorded in
these attempts. The audit therefore does not claim that Kiln incorrectly
certified those tests or that changing pipeline semantics would have prevented
the submitted code defects.

## Remaining questions

- Anthropic's request builder currently computes strict-tool support without
  emitting it. Enabling schema-constrained calls requires a separate provider
  and schema compatibility check; this patch does not enable it globally.
- Instruction parity still needs coverage for native AGENTS.md fallback,
  ancestor/local instructions, child directory symlinks, and conditional
  path-scoped rule loading. Nested discovery alone does not implement those
  lifecycle behaviors. This repository explicitly imports AGENTS.md through
  CLAUDE.md, so that fallback gap did not omit its benchmark instructions.
- No reproducible compaction defect was found in this pass. Future controlled
  evaluations should exercise long tasks, model switching, and learned state
  rather than infer their quality from the short memory-import cohort.
- Cost optimizations still need held-out output-quality comparisons. Passing
  these regression tests proves the specific repairs, not quality parity with
  Claude Code or a general improvement in generated code.

The fixes and red/green evidence are recorded under `qa/findings/` with slugs
`nested-rules-omitted`, `anthropic-redacted-replay`, and
`memory-source-fifo-blocks`.

Validation on Go 1.26.6: `make check`, full `make e2e` (233.376 seconds),
changed-package race tests, `make covercheck`, pinned golangci-lint on the new
changes, and Windows compilation of the memory test package passed. Coverage
floors were unchanged. The next quality comparison has not been run.
