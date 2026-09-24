// Package keybindings ports harness/src/claude/keybindings.ts: reading
// ~/.claude/keybindings.json into a data-only Result (Loaded, Bindings,
// Conflicts, Error). Conflicts — two actions bound to the same key — are
// reported, never resolved; applying the bindings and settling conflicts
// is the TUI's job, not this package's.
package keybindings
