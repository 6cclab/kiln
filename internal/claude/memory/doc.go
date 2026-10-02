// Package memory ports harness/src/claude/memory.ts: loading CLAUDE.md
// memory across the user/project scopes plus .claude/rules/*.md,
// resolving @path imports (cycle- and depth-guarded), and applying a
// token budget that drops the least specific content first. kiln reads
// these files but never writes any of them - there is no kiln-authored
// CLAUDE.md or "#note" input mode; Claude Code's own prompt input has no
// such shortcut either (only a bash prefix and a plain prompt), so kiln
// dropped it to match.
//
// automemory.go reads Claude Code's own auto memory
// (~/.claude/projects/<project>/memory/MEMORY.md) read-only: kiln never
// writes there (see docs/configuration.md and
// ~/.claude/plans/kiln-self-improvement.md's "Claude Code parity"
// section). There is no TS/JS source to port this from — auto memory postdates
// this codebase's pi port — so it is implemented straight from Claude
// Code's own docs (https://code.claude.com/docs/en/memory).
package memory
