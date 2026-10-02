// Package memory ports harness/src/claude/memory.ts: loading CLAUDE.md
// memory across the user/project scopes plus .claude/rules/*.md,
// resolving @path imports (cycle- and depth-guarded), and applying a
// token budget that drops the least specific content first.
//
// AddMemory ports harness/src/tui/input-modes.ts's addMemory (the "#note"
// input mode) rather than anything in memory.ts, which has no such
// function; it is grouped here because it operates on the same CLAUDE.md
// files this package reads.
//
// automemory.go reads Claude Code's own auto memory
// (~/.claude/projects/<project>/memory/MEMORY.md) read-only: kiln never
// writes there (see docs/configuration.md and
// ~/.claude/plans/kiln-self-improvement.md's "Claude Code parity"
// section). There is no TS/JS source to port this from — auto memory postdates
// this codebase's pi port — so it is implemented straight from Claude
// Code's own docs (https://code.claude.com/docs/en/memory).
package memory
