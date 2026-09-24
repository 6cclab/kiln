// Package memory ports harness/src/claude/memory.ts: loading CLAUDE.md
// memory across the user/project scopes plus .claude/rules/*.md,
// resolving @path imports (cycle- and depth-guarded), and applying a
// token budget that drops the least specific content first.
//
// AddMemory ports harness/src/tui/input-modes.ts's addMemory (the "#note"
// input mode) rather than anything in memory.ts, which has no such
// function; it is grouped here because it operates on the same CLAUDE.md
// files this package reads.
package memory
