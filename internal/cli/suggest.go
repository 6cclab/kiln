// Package cli, this file: the prompt-suggestion model call's wording and the
// one-line cleanup its reply goes through. RunInteractive's SuggestNextPrompt
// (built at chat.go's TUI seam) uses these; the TUI shows the result as dim
// ghost text in the empty input box, Tab to accept (Claude Code's prompt
// suggestion).
package cli

import (
	"os"
	"strings"
)

// promptSuggestionsEnabled resolves whether to generate next-prompt
// suggestions, matching Claude Code's precedence: the
// CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION env var wins when it is a recognised
// boolean, then the promptSuggestionEnabled setting, then off by default (as
// Claude Code leaves them off in interactive mode). Off means kiln makes no
// extra model call and shows no ghost text.
func promptSuggestionsEnabled(setting *bool) bool {
	if v, ok := os.LookupEnv("CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION"); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	if setting != nil {
		return *setting
	}
	return false
}

// suggestSystemPrompt steers the session model to predict, not to do. It asks
// for one short imperative line the user would plausibly type next, and for
// nothing at all when there is no sensible follow-up (an empty reply, which
// cleanSuggestion maps to "no suggestion").
const suggestSystemPrompt = `You predict the next message a developer will type to their coding agent.
Given the conversation so far, reply with a single short prompt the developer is likely to send next — an imperative instruction or a question, at most about ten words.
Rules:
- Output only the prompt text. No quotes, no "You could:", no explanation, no trailing punctuation beyond a question mark.
- Make it a natural continuation of the work, not a restatement of what was just done.
- If there is no sensible next prompt, output nothing at all.`

// suggestPromptInstruction is the trailing user turn that asks for the guess,
// so the model answers as its own assistant turn rather than continuing the
// previous one.
const suggestPromptInstruction = "Based on everything above, what is the single most likely next prompt I would type? Reply with only that prompt, or nothing if there is no good suggestion."

// cleanSuggestion reduces a model reply to one presentable ghost line: the
// first non-empty line, with surrounding quotes and a leading list marker
// stripped, capped so it cannot overflow the input box. An empty or
// refusal-shaped reply becomes "" (no suggestion).
func cleanSuggestion(raw string) string {
	line := ""
	for _, l := range strings.Split(raw, "\n") {
		if strings.TrimSpace(l) != "" {
			line = strings.TrimSpace(l)
			break
		}
	}
	// Strip a leading list marker ("- ", "* ", "1. ").
	line = strings.TrimLeft(line, "-*0123456789. \t")
	// Strip one layer of matching surrounding quotes.
	line = trimMatchingQuotes(line)
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	// A model that "says nothing" sometimes says so in words; treat the
	// common shapes as no suggestion rather than showing them as a prompt.
	switch strings.ToLower(line) {
	case "nothing", "none", "n/a", "no suggestion", "(none)":
		return ""
	}
	const maxRunes = 120
	if r := []rune(line); len(r) > maxRunes {
		line = strings.TrimRight(string(r[:maxRunes]), " ") + "…"
	}
	return line
}

func trimMatchingQuotes(s string) string {
	if len(s) < 2 {
		return s
	}
	first, last := s[0], s[len(s)-1]
	if (first == '"' && last == '"') || (first == '\'' && last == '\'') || (first == '`' && last == '`') {
		return s[1 : len(s)-1]
	}
	return s
}
