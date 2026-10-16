package router

import (
	"regexp"
	"strings"
)

// Heuristic is the opt-in fallback for when the classifier is off,
// unavailable, or gives no answer and modelRouter.fallback is
// "heuristic" (settings.RouterFallbackHeuristic). It is a pure function
// of the typed text, so @file expansions never reach it: Auto.rate passes
// it the same text the classifier would have seen (and the same 16,000
// character clip).
//
// The request counts as Complex if any of these hold:
//   - it is at least heuristicMinChars characters long
//   - it names at least heuristicMinPaths distinct path-like tokens
//   - it has a fenced code block or a stack trace of at least
//     heuristicMinBlockLines lines
//   - it matches one of heuristicStems
//
// Otherwise it is Standard.
func Heuristic(prompt string) Complexity {
	if len([]rune(prompt)) >= heuristicMinChars {
		return Complex
	}
	if len(distinctPaths(prompt)) >= heuristicMinPaths {
		return Complex
	}
	if hasLongFence(prompt) || hasLongTrace(prompt) {
		return Complex
	}
	if heuristicStemRE.MatchString(strings.ToLower(prompt)) {
		return Complex
	}
	return Standard
}

const (
	heuristicMinChars      = 1200
	heuristicMinPaths      = 4
	heuristicMinBlockLines = 40
)

// heuristicStems are kiln's own additions to jev's classifier (jev has no
// heuristic fallback): stems whose presence alone marks a request
// complex, whichever case they are written in.
var heuristicStemRE = regexp.MustCompile(`\b(architect\w*|design\w*|refactor\w*|migrat\w*|race\w*|deadlock\w*|concurren\w*|root cause\w*|investigat\w*|flaky\w*|performance\w*|security\w*|across the\w*)\b`)

// pathTokenRE matches a path-like token: at least two segments separated
// by "/", each a run of ordinary filename characters. It undercounts a
// bare "a/b" as a path even outside of code, which is the same trade-off
// jev's own heuristics make elsewhere: a cheap signal, not a parser.
var pathTokenRE = regexp.MustCompile(`[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)+`)

// distinctPaths lists the distinct path-like tokens in prompt.
func distinctPaths(prompt string) map[string]bool {
	out := map[string]bool{}
	for _, m := range pathTokenRE.FindAllString(prompt, -1) {
		out[m] = true
	}
	return out
}

// hasLongFence reports whether prompt has a fenced code block (```) of at
// least heuristicMinBlockLines lines between its fences.
func hasLongFence(prompt string) bool {
	lines := strings.Split(prompt, "\n")
	open := false
	count := 0
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if open {
				if count >= heuristicMinBlockLines {
					return true
				}
				open = false
				count = 0
			} else {
				open = true
				count = 0
			}
			continue
		}
		if open {
			count++
		}
	}
	// An unterminated fence (the prompt was clipped mid-block) still
	// shows its lines so far.
	return open && count >= heuristicMinBlockLines
}

// traceLineRE matches a line that looks like it belongs to a stack trace
// or exception log: a language's "at", a Python traceback frame, a
// "file:line" reference, or a numbered native frame.
var traceLineRE = regexp.MustCompile(`^\s*(at\s+\S|File "|#\d+\s|0x[0-9a-fA-F]+\s)|\.\w+:\d+(:\d+)?\)?\s*$`)

// hasLongTrace reports whether prompt has a run of at least
// heuristicMinBlockLines consecutive lines that look like a stack trace.
func hasLongTrace(prompt string) bool {
	run := 0
	for _, line := range strings.Split(prompt, "\n") {
		if traceLineRE.MatchString(line) {
			run++
			if run >= heuristicMinBlockLines {
				return true
			}
			continue
		}
		if strings.TrimSpace(line) == "" {
			// A blank line inside a trace does not break the run.
			continue
		}
		run = 0
	}
	return false
}
