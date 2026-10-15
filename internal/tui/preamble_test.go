package tui

import "testing"

// TestStripPromptPreamble: replay and rewind show what the user typed, not
// the reminders and hook context kiln sent ahead of it.
func TestStripPromptPreamble(t *testing.T) {
	for in, want := range map[string]string{
		"fix add": "fix add",
		"<system-reminder>\nYou are in PLAN MODE.\n</system-reminder>\n\nfix add":                   "fix add",
		"<system-reminder>\na\n</system-reminder>\n\n<hook-context>\nb\n</hook-context>\n\nfix add": "fix add",
		"<hook-context>\nunclosed":       "<hook-context>\nunclosed",
		"explain <system-reminder> tags": "explain <system-reminder> tags",
	} {
		if got := stripPromptPreamble(in); got != want {
			t.Errorf("stripPromptPreamble(%q) = %q, want %q", in, got, want)
		}
	}
}
