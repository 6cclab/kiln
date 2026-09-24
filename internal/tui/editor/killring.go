package editor

import "unicode"

// maxKillRingEntries bounds the ring pi-tui calls a "kill ring" — Emacs's
// term for the same structure. Small, because the editor only ever needs to
// recover the last handful of kills; unbounded growth here would just be a
// memory leak with no user-visible benefit.
const maxKillRingEntries = 8

// killRing is a small, most-recent-first ring of killed text. Ctrl+K
// (kill to end of line), Ctrl+U (kill to start of line) and Ctrl+W /
// Alt+Backspace (kill word backward) push onto it before deleting; Ctrl+Y
// yanks the most recent entry back in, without consuming it, so repeated
// Ctrl+Y re-inserts the same text (standard kill-ring behaviour, not a
// yank-pop cycle — the editor.md task list only asks for a single yank
// key, so cycling through older kills is left undone; see doc.go).
type killRing struct {
	entries []string
}

func (k *killRing) push(s string) {
	if s == "" {
		return
	}
	k.entries = append([]string{s}, k.entries...)
	if len(k.entries) > maxKillRingEntries {
		k.entries = k.entries[:maxKillRingEntries]
	}
}

func (k *killRing) latest() (string, bool) {
	if len(k.entries) == 0 {
		return "", false
	}
	return k.entries[0], true
}

// wordBackStart finds the start of the word being killed backward from
// col, ported rune-for-rune from bubbles/v2 textarea's deleteWordLeft
// (textarea.go) so the text this pushes onto the kill ring is exactly the
// text that deletion is about to remove: skip whitespace immediately
// before the cursor, then skip the word, but keep (do not consume) one
// space that precedes the word.
func wordBackStart(line []rune, col int) int {
	if col <= 0 || len(line) == 0 {
		return 0
	}
	if col > len(line) {
		col = len(line)
	}
	c := col - 1
	for unicode.IsSpace(line[c]) {
		if c <= 0 {
			break
		}
		c--
	}
	for c > 0 {
		if !unicode.IsSpace(line[c]) {
			c--
		} else {
			c++
			break
		}
	}
	return c
}
