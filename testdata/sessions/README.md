# Session fixtures

Three real harness session transcripts, scrubbed, used as fixtures for the
Go session-store and transcript-rendering tests.

- `2026-09-23T11-37-47-498Z_01a0ce0e-bcea-7701-a97e-cc374e8c56d1.jsonl` — a
  large session (1090 lines) from a real `harness` working session.
- `2026-09-23T13-15-57-415Z_01a0ce68-9c67-7740-9867-7150069d61e6.jsonl` — a
  minimal session (2 lines: header + initial lane state) from the same
  project.
- `2026-09-23T04-00-59-438Z_01a0cc6c-862e-70d7-b46f-cf4005693013.jsonl` — a
  session from a throwaway demo project, exercising a different `cwd` and
  tool set (including several MCP servers).
- `legacy-v3-fixture.jsonl` — **synthetic**, not scrubbed output of a real
  session. There is no real legacy v3 (`{"type":"session","version":3,...}`)
  session file on this machine to scrub, so this fixture was constructed by
  hand from pi's `legacy-v3.d.ts` types and `legacy-v3.js`'s parsing
  expectations (see `internal/session/jsonl/legacy_v3.go` for the mapping it
  exercises). It has a header, a user/assistant/toolResult turn, a
  `model_change`/`thinking_level_change`/`active_tools_change` run, a branch
  (two children of one entry — one abandoned, one continued), a `custom`
  entry, a `compaction` (whose retained tail reconstruction spans the
  branch), a `branch_summary` referencing the abandoned branch, a `label`,
  and a `session_info`. Used only by
  `internal/session/jsonl/legacy_v3_test.go`.

## Scrubbing

Each file is the output of `sessiondata.Scrub`
(`internal/testkit/sessiondata/scrub.go`) run against the original session
file under `~/.harness/sessions/<project>/`. Scrub preserves line count,
header shape, and every id/seq/timestamp/namespace/key/kind/type/role/model
name/tool name, while:

- redacting API keys, bearer tokens, GitHub and Slack tokens, and email
  addresses wherever they appear;
- rewriting absolute paths and bare mentions of the operator's home
  directory/username to `/Users/tester` / `tester`;
- truncating any other string value over 200 characters (that isn't an
  id/key/type/kind/namespace-like field) to its first 40 characters plus a
  `…[scrubbed N chars]` marker.

Only the scrubbed output is committed; the original session files never
leave `~/.harness/sessions`.

## Regenerating

To regenerate a fixture from a fresh session file, write a small program
(or a throwaway test) that opens the source file and calls:

```go
sessiondata.Scrub(sourceFile, destFile)
```

`internal/testkit/sessiondata/scrub_test.go` runs the same function against
the current real session files (skipped if `~/.harness/sessions` isn't
present) and asserts that regenerating them would produce output with the
same invariants that these committed fixtures already satisfy: identical
line count, identical header fields (other than the scrubbed `cwd`),
identical `(kind,type,namespace)` triples, and no leftover `andrepato`.
