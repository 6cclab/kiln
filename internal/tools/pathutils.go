package tools

import (
	"regexp"
	"strings"

	"github.com/andrepato/harness/internal/execenv"
)

// unicodeSpaces matches the special Unicode space characters pi normalizes
// out of tool-supplied paths, mirroring path-utils.js's UNICODE_SPACES.
var unicodeSpaces = regexp.MustCompile("[  -   　]")

const narrowNoBreakSpace = " "

// normalizeToolPath mirrors path-utils.js's normalizeToolPath: collapse
// Unicode space variants to plain spaces, and strip a leading "@" (some
// clients quote paths with an @-mention prefix).
func normalizeToolPath(path string) string {
	normalized := unicodeSpaces.ReplaceAllString(path, " ")
	return strings.TrimPrefix(normalized, "@")
}

// resolveToolPath mirrors path-utils.js's resolveToolPath.
func resolveToolPath(env *execenv.Env, path string) string {
	return env.AbsolutePath(normalizeToolPath(path))
}

// amPmSpace matches " AM." / " PM." so resolveReadToolPath can retry with
// macOS Photos-style narrow-no-break-space timestamps.
var amPmSpace = regexp.MustCompile(`(?i) (AM|PM)\.`)

// resolveReadToolPath mirrors path-utils.js's resolveReadToolPath: some
// filenames arrive with characters the model normalized away (a narrow
// no-break space before AM/PM, precomposed vs. decomposed Unicode, a
// curly apostrophe standing in for a straight one). Try the literal path
// first, then each of those variants, falling back to the literal
// resolved path if none exist so callers get pi's ordinary "not found"
// error instead of a silent substitution.
func resolveReadToolPath(env *execenv.Env, path string) string {
	resolved := resolveToolPath(env, path)
	seen := map[string]bool{}
	variants := []string{
		resolved,
		amPmSpace.ReplaceAllString(resolved, " "+narrowNoBreakSpace+"$1."),
		normalizeNFD(resolved),
		strings.ReplaceAll(resolved, "'", "’"),
		strings.ReplaceAll(normalizeNFD(resolved), "'", "’"),
	}
	for _, variant := range variants {
		if seen[variant] {
			continue
		}
		seen[variant] = true
		if ok, err := env.Exists(variant); err == nil && ok {
			return variant
		}
	}
	return resolved
}

// normalizeNFD is a deviation from pi's resolveReadToolPath, which retries
// with path.normalize("NFD") (Unicode canonical decomposition — e.g. an
// "é" precomposed as U+00E9 becomes "e" + a combining acute accent). Go's
// standard library has no Unicode normalization package, and adding
// golang.org/x/text for this one macOS-filename edge case was judged not
// worth a new dependency, so this is the identity function: the
// NFD-decomposed retry variant is skipped, and the other retries (curly
// apostrophe, narrow-no-break-space before AM/PM) still apply.
func normalizeNFD(path string) string {
	return path
}
