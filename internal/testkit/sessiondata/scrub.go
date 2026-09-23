// Package sessiondata scrubs harness session transcripts (the .jsonl files
// under ~/.harness/sessions/<project>/) so that real, potentially sensitive
// session recordings can be checked into testdata/sessions and used as
// fixtures for the Go port's session-store and transcript-rendering tests.
//
// Scrub preserves the exact line-by-line structure of a session file (one
// JSON value per line: a header object on line 1, then arrays of "value"
// and "entry" operations on every following line) along with every field
// that later code needs to key off of — ids, seq numbers, timestamps,
// namespaces, keys, kinds/types, model names, tool names and roles — while
// redacting anything that could leak a real secret, a real absolute path
// under the operator's home directory, or an unbounded amount of real
// prose/code from a real session.
package sessiondata

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// maxLineLen bounds the longest session line Scrub will accept. Real
// session lines are a few KB; this is generous headroom for large tool
// outputs while still catching corrupt input.
const maxLineLen = 64 * 1024 * 1024

// maxKeptChars is how many leading characters of an over-long string value
// are kept before it is truncated and annotated with a scrub marker.
const maxKeptChars = 40

// maxStringLen is the length above which a string value (that isn't an
// id/key/type/kind/namespace-like field) is truncated.
const maxStringLen = 200

var (
	secretKeyPattern = regexp.MustCompile(`sk-[A-Za-z0-9]{10,}`)
	bearerPattern    = regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._\-]+`)
	ghpPattern       = regexp.MustCompile(`ghp_[A-Za-z0-9]+`)
	xoxPattern       = regexp.MustCompile(`xox[a-zA-Z0-9]-[A-Za-z0-9\-]+`)
	emailPattern     = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	homePathPattern  = regexp.MustCompile(`/Users/andrepato\b`)
	// usernamePattern catches bare mentions of the operator's username that
	// aren't part of an absolute path — e.g. `ls -l` owner columns
	// ("-rw-r--r--  1 andrepato  staff") or prose ("I am andrepato").
	// homePathPattern above already rewrites the path form to
	// "/Users/tester", so by the time this runs any remaining "andrepato"
	// is a bare username and is replaced with "tester" to match.
	usernamePattern = regexp.MustCompile(`\bandrepato\b`)
)

// exemptSubstrings are lowercase substrings that mark a JSON object key as
// "id-like": a field whose string value is an identifier, kind, type,
// namespace, key or similar, never truncated even when long (ids can
// legitimately run past 40-ish characters, e.g. ULIDs embedded in
// composite ids).
var exemptSubstrings = []string{"namespace", "key", "type", "kind", "id"}

// Scrub reads a harness session .jsonl file from in and writes a scrubbed
// copy to out, preserving line count, header shape, and every
// (kind,type,namespace) pair, while redacting secrets, emails, home-
// directory paths and over-long prose/code strings.
func Scrub(in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineLen)

	w := bufio.NewWriter(out)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			if _, err := w.Write(line); err != nil {
				return err
			}
			if err := w.WriteByte('\n'); err != nil {
				return err
			}
			continue
		}

		scrubbed, err := scrubLine(line)
		if err != nil {
			return fmt.Errorf("sessiondata: scrubbing line %d: %w", lineNo, err)
		}
		if _, err := w.Write(scrubbed); err != nil {
			return err
		}
		if err := w.WriteByte('\n'); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("sessiondata: reading input: %w", err)
	}
	return w.Flush()
}

// scrubLine decodes a single JSON line (an object for the header, or an
// array of operations for every following line), scrubs it, and
// re-encodes it.
func scrubLine(line []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()

	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("decoding JSON: %w", err)
	}

	scrubbed := scrubNode(v, false)

	b, err := json.Marshal(scrubbed)
	if err != nil {
		return nil, fmt.Errorf("re-encoding JSON: %w", err)
	}
	return b, nil
}

// scrubNode walks a decoded JSON value (map[string]any, []any, string,
// json.Number, bool or nil) and returns a scrubbed copy. exemptFromTruncate
// is true when this value was reached under a key that marks it as
// id/key/type/kind/namespace-like: such string values are still redacted
// for secrets/paths/emails, but never truncated for length.
func scrubNode(v any, exemptFromTruncate bool) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = scrubNode(val, isExemptKey(k))
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = scrubNode(e, exemptFromTruncate)
		}
		return out
	case string:
		return scrubString(t, exemptFromTruncate)
	default:
		// json.Number, bool, nil: pass through unchanged.
		return v
	}
}

// isExemptKey reports whether a JSON object key names an id/key/type/kind/
// namespace-like field, whose string value should never be truncated for
// length (though it is still redacted for secrets/paths/emails).
func isExemptKey(key string) bool {
	lk := strings.ToLower(key)
	for _, s := range exemptSubstrings {
		if strings.Contains(lk, s) {
			return true
		}
	}
	return false
}

// scrubString redacts anything that looks like a secret, bearer token,
// email address or home-directory path from s, then — unless exempt —
// truncates it if it is longer than maxStringLen, keeping the first
// maxKeptChars characters and noting how many characters were scrubbed.
func scrubString(s string, exempt bool) string {
	s = redactPatterns(s)
	if exempt {
		return s
	}
	if len(s) <= maxStringLen {
		return s
	}
	kept := s[:maxKeptChars]
	scrubbedChars := len(s) - maxKeptChars
	return fmt.Sprintf("%s…[scrubbed %d chars]", kept, scrubbedChars)
}

// redactPatterns replaces API-key-shaped, bearer-token-shaped, GitHub-
// token-shaped, Slack-token-shaped, email-shaped and home-directory-path
// substrings within s. It runs on every string value, exempt or not,
// because a secret or a real path must never survive scrubbing regardless
// of which field it appears in.
func redactPatterns(s string) string {
	s = secretKeyPattern.ReplaceAllString(s, "sk-[scrubbed]")
	s = bearerPattern.ReplaceAllString(s, "Bearer [scrubbed]")
	s = ghpPattern.ReplaceAllString(s, "ghp_[scrubbed]")
	s = xoxPattern.ReplaceAllString(s, "xox-[scrubbed]")
	s = emailPattern.ReplaceAllString(s, "scrubbed@example.com")
	s = homePathPattern.ReplaceAllString(s, "/Users/tester")
	s = usernamePattern.ReplaceAllString(s, "tester")
	return s
}
