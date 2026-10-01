package execenv

import (
	"regexp"
	"strings"
)

// unicodeSpaces matches the special Unicode space characters pi normalizes
// out of tool-supplied paths, mirroring path-utils.js's UNICODE_SPACES.
var unicodeSpaces = regexp.MustCompile("[  -   　]")

// NormalizeToolPath mirrors path-utils.js's normalizeToolPath: collapse
// Unicode space variants to plain spaces, and strip a leading "@" (some
// clients quote paths with an @-mention prefix).
func NormalizeToolPath(path string) string {
	normalized := unicodeSpaces.ReplaceAllString(path, " ")
	return strings.TrimPrefix(normalized, "@")
}

// ResolveToolPath is the absolute path a file tool's path argument names:
// NormalizeToolPath, then "~", "~/" and file:// expansion, then relative
// to cwd, cleaned. Symlinks are not resolved. The file tools and the
// permission gate both use it, so a rule is judged against the file the
// tool will actually open, not the raw text the model sent.
func ResolveToolPath(cwd, path string) string {
	return resolvePath(cwd, NormalizeToolPath(path))
}
