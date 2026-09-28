// Package notes is a tiny package used to exercise kiln's manual permission
// prompts (one file edit, one bash command) rather than to assert specific
// behavior of its own.
package notes

// Count returns the number of notes in ns.
func Count(ns []string) int {
	return len(ns)
}
