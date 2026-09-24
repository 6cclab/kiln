package editor

import (
	"os"
	"path/filepath"
	"strings"
)

// maxHistoryEntries matches src/tui/history.ts's MAX_ENTRIES.
const maxHistoryEntries = 500

// Path is the default history file location, ported from history.ts's
// historyPath(): under ~/.harness, not the project, because a prompt can
// contain anything typed into it and a repo directory invites committing
// that.
func Path() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".harness", "history")
}

// Load reads the history file, oldest entry first (the order Append writes
// them and the order the editor's Up/Down navigation expects: newest last).
// A missing file is the normal first run, not an error — it returns nil.
// Each line is one entry with embedded newlines escaped as `\n`; unescaped
// and trimmed here, matching loadHistory in history.ts.
func Load(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	entries := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.ReplaceAll(line, `\n`, "\n")
		line = strings.TrimSpace(line)
		if line != "" {
			entries = append(entries, line)
		}
	}
	if len(entries) > maxHistoryEntries {
		entries = entries[len(entries)-maxHistoryEntries:]
	}
	return entries
}

// Append adds one entry to the history file, matching appendHistory in
// history.ts: appended rather than rewritten, so two sessions running at
// once interleave instead of one truncating the other's history; blank
// input is ignored; a multi-line entry is written as one line with `\n`
// escaped, so a pasted block becomes one history entry instead of one per
// line; and any failure (unwritable path, missing permissions) is
// swallowed, because history is a convenience that must never cost the
// turn the user just submitted.
func Append(path, entry string) {
	text := strings.TrimSpace(entry)
	if text == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	escaped := strings.ReplaceAll(text, "\n", `\n`)
	_, _ = f.WriteString(escaped + "\n")
}
