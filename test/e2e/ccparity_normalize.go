//go:build e2e

package e2e

// ccparity_normalize.go: the normaliser ccparity_test.go runs both a
// reference Claude Code screen (loaded verbatim from
// testdata/reference/claude-code/*.txt) and the harness's own captured
// screen through, so the two are comparable despite carrying different
// machine-specific and non-deterministic values. See docs/testing.md's
// "ccparity normaliser rules" section for the human-readable version of
// this list; keep the two in sync.
//
// Deliberately NOT normalised: indentation, column positions, glyphs,
// wording, row order. Those are exactly what this suite exists to catch a
// difference in.

import (
	"regexp"
	"strings"
)

var (
	reVersion    = regexp.MustCompile(`\bv\d+(\.\d+){1,3}\b`)
	reClock      = regexp.MustCompile(`\b(0?[1-9]|1[0-2]):[0-5][0-9]\s?(AM|PM|am|pm)\b`)
	reForN       = regexp.MustCompile(`\bfor \d+s\b`)
	reTokenCount = regexp.MustCompile(`\b\d+(\.\d+)?[km]?/\d+(\.\d+)?[km]?\b`)
	reCost       = regexp.MustCompile(`\$\d+(\.\d+)?`)
	rePercent    = regexp.MustCompile(`\b\d+%`)

	// Claude Code's model display ("Opus 5 (1M context)", "claude-opus-5")
	// and the harness's own "<provider>/<model>" display both collapse to
	// MODEL, so a scenario compares layout, not which model each side
	// happens to be configured with. reModelDisplay is deliberately
	// anchored to the harness's known provider prefixes rather than a
	// generic "word/word" pattern: the reference screens' cwd rows
	// (e.g. ".../var/folders/93/T/cc-ref/proj") also look like
	// "word/word" and were getting swallowed by an earlier, broader
	// version of this regex — a real bug, not a hypothetical one, caught
	// by running this suite against the reference corpus (see this
	// suite's report).
	reModelDisplay = regexp.MustCompile(`\b(faux|ollama|anthropic|openai|google|groq|xai|bedrock)/[A-Za-z0-9][A-Za-z0-9.-]*\b`)
	reCCModelName  = regexp.MustCompile(`\b(Opus|Sonnet|Haiku|Fable) [0-9][0-9.]*( \(\d+[MK] context\))?\b`)
	reCCModelSlug  = regexp.MustCompile(`\bclaude-[a-z0-9-]+\b`)

	// reRefCaptureCWD matches the fixed working-directory path every
	// screen in testdata/reference/claude-code was captured under (see
	// that directory's README's "Recapture" note: a scratch repo under
	// $TMPDIR/cc-ref). It is a second, independent CWD substitution from
	// the cwd parameter normalizeScreen takes, because the two sides of a
	// comparison have two different real cwds (the reference machine's
	// and this run's scratch project dir), each only known on its own
	// side.
	reRefCaptureCWD = regexp.MustCompile(`(/private)?/var/folders/[^\s]+/T/cc-ref/proj`)
)

// dropPrefixes: a row whose trimmed-left content starts with one of these
// is Claude Code product content out of scope for the harness (see
// docs/claude-code-reference.md's "Out of scope" note) and is dropped
// entirely from both sides before comparison, rather than normalised.
var dropPrefixes = []string{
	"⎿  Tip:",
	"▎ ※ Claim",
	"Get to finished work sooner",
	"⚠ 1 MCP server needs authentication",
	"⚠ 2 MCP server",
}

// normalizeScreen maps rows (from either the reference .txt or the
// harness's own s.Rows()) to a comparable form: trailing spaces stripped,
// machine-specific/non-deterministic substrings collapsed to placeholders,
// and Claude Code product-content rows dropped. cwd, when non-empty, is
// also replaced with CWD (the reference screens' cwd is a captured
// tmpdir path; the harness's is the scratch project directory, and
// neither is stable across runs).
func normalizeScreen(rows []string, cwd string) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		r = strings.TrimRight(r, " ")
		if dropRow(strings.TrimLeft(r, " ")) {
			continue
		}
		out = append(out, normalizeRow(r, cwd))
	}
	return out
}

func dropRow(trimmed string) bool {
	for _, p := range dropPrefixes {
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}
	// The statusLine row this machine's settings.json adds
	// ("Opus 5 (1M context) │ ⎇ main ✔ │ …") is this machine's config,
	// not part of the rendering contract (see
	// docs/claude-code-reference.md section 1).
	if strings.Contains(trimmed, "│ ⎇ ") {
		return true
	}
	return false
}

func normalizeRow(r, cwd string) string {
	if cwd != "" {
		r = strings.ReplaceAll(r, cwd, "CWD")
	}
	r = reRefCaptureCWD.ReplaceAllString(r, "CWD")
	r = reVersion.ReplaceAllString(r, "vX")
	r = reCCModelSlug.ReplaceAllString(r, "MODEL")
	r = reCCModelName.ReplaceAllString(r, "MODEL")
	r = reModelDisplay.ReplaceAllString(r, "MODEL")
	r = reClock.ReplaceAllString(r, "HH:MM XM")
	r = reForN.ReplaceAllString(r, "for Ns")
	r = reTokenCount.ReplaceAllString(r, "N tokens")
	r = reCost.ReplaceAllString(r, "$N")
	r = rePercent.ReplaceAllString(r, "N%")
	return r
}
