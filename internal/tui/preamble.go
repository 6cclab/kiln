package tui

import "strings"

// preambleTags are the blocks kiln puts ahead of a typed prompt before it
// goes to the model: mode reminders (internal/cli/modereminders.go) and
// hook context (handleSubmit). The user never typed them, so a replayed or
// rewound prompt shows only what follows them.
var preambleTags = []string{"system-reminder", "hook-context"}

// stripPromptPreamble removes the leading preamble blocks from a stored
// prompt, leaving the text the user typed.
func stripPromptPreamble(text string) string {
	for {
		trimmed := strings.TrimLeft(text, "\n")
		stripped := false
		for _, tag := range preambleTags {
			open, closing := "<"+tag+">", "</"+tag+">"
			if !strings.HasPrefix(trimmed, open) {
				continue
			}
			end := strings.Index(trimmed, closing)
			if end < 0 {
				return text
			}
			text = trimmed[end+len(closing):]
			stripped = true
			break
		}
		if !stripped {
			return strings.TrimLeft(text, "\n")
		}
	}
}
