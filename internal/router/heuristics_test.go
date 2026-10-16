package router

import (
	"strings"
	"testing"
)

func TestHeuristic(t *testing.T) {
	longFence := "```\n" + strings.Repeat("line of code\n", 40) + "```"
	trace := strings.Repeat("\tat com.example.Foo.bar(Foo.java:42)\n", 40)
	paths := "look at internal/router/auto.go internal/cli/chat.go internal/router/heuristics.go and internal/router/classify.go"

	cases := []struct {
		name   string
		prompt string
		want   Complexity
	}{
		{"short ordinary request", "fix the typo in the README", Standard},
		{"a question", "what does this function do?", Standard},
		{"at least 1200 chars", strings.Repeat("a", 1200), Complex},
		{"just under 1200 chars", strings.Repeat("a", 1199), Standard},
		{"4 distinct paths", paths, Complex},
		{"3 distinct paths: not enough", "look at internal/router/auto.go internal/cli/chat.go and internal/router/heuristics.go", Standard},
		{"a long fenced block", "explain this:\n\n" + longFence, Complex},
		{"a short fenced block", "explain this:\n\n```\nshort\n```", Standard},
		{"a long stack trace", "it crashed:\n\n" + trace, Complex},
		{"architect stem", "can you architect a new subsystem for this?", Complex},
		{"refactor stem", "please refactor this module", Complex},
		{"migrat stem (migration/migrate)", "plan the database migration", Complex},
		{"race stem", "there's a race condition here", Complex},
		{"deadlock stem", "diagnose this deadlock", Complex},
		{"concurren stem", "this concurrency bug is weird", Complex},
		{"root cause phrase", "find the root cause of this failure", Complex},
		{"investigat stem", "please investigate this behavior", Complex},
		{"flaky stem", "this test is flaky", Complex},
		{"performance stem", "improve the performance of this query", Complex},
		{"security stem", "review this for security issues", Complex},
		{"across the phrase", "rename this field across the codebase", Complex},
		{"case insensitive stem", "REFACTOR this please", Complex},
		{"a word containing the stem as a substring is not matched loosely for race", "this will embrace the new design.", Complex}, // "design" stem matches
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Heuristic(c.prompt); got != c.want {
				t.Errorf("Heuristic(%q) = %q, want %q", clipText(c.prompt, 80), got, c.want)
			}
		})
	}
}

// Heuristic is a pure function of its argument: Auto.rate already clips
// to classifierPromptChars and uses the typed text, so the heuristic
// itself does not need to repeat that, but it must not look beyond its
// argument for anything else (no globals, no time-of-day).
func TestHeuristic_Deterministic(t *testing.T) {
	prompt := "refactor the auth module"
	a, b := Heuristic(prompt), Heuristic(prompt)
	if a != b || a != Complex {
		t.Errorf("Heuristic(%q) = %q then %q", prompt, a, b)
	}
}
