package execenv

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// amPmSpace matches " AM." / " PM." so ReadPathVariants can retry with
// macOS Photos-style narrow-no-break-space timestamps.
var amPmSpace = regexp.MustCompile(`(?i) (AM|PM)\.`)

const (
	narrowNoBreakSpace = " "
	curlyApostrophe    = "’"
)

// ReadPathVariants is every path the read tool may open for the resolved
// path abs, in the order it tries them (path-utils.js's
// resolveReadToolPath): abs itself, then the narrow-no-break-space and
// curly-apostrophe spellings. pi also retries the NFD-decomposed form;
// kiln does not (normalizeNFD was the identity), so neither does this.
// Duplicates are dropped.
func ReadPathVariants(abs string) []string {
	candidates := []string{
		abs,
		amPmSpace.ReplaceAllString(abs, " "+narrowNoBreakSpace+"$1."),
		strings.ReplaceAll(abs, "'", curlyApostrophe),
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range candidates {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// maxLinkHops bounds symlink resolution, as the kernel's ELOOP does.
const maxLinkHops = 40

// RealPath resolves every symlink in the absolute path p, one component
// at a time, the way the kernel walks it on open: a link's target is
// pushed onto the walk as written (never cleaned), and ".." steps to the
// real parent of what has resolved so far. So with "sshl -> ~/.ssh" and
// "evil -> sshl/..", evil is ~ and evil/.ssh/x is ~/.ssh/x, as open(2)
// sees it, not the lexical <dir>/.ssh/x. p itself
// is not cleaned first either: "link/../x" climbs out of the link's
// target, as a shell's open of that path does.
//
// Unlike filepath.EvalSymlinks it follows a dangling link (a link whose
// target does not exist yet): writing through "dangle ->
// ~/.ssh/authorized_keys" creates the target, so the target is the path
// that matters. Components under one that does not exist are taken as
// written. ok is false when the path cannot be resolved (a symlink loop or
// an unreadable link); p is then returned cleaned.
func RealPath(p string) (string, bool) {
	if !filepath.IsAbs(p) {
		return filepath.Clean(p), false
	}
	queue := splitPath(p)
	var cur []string // resolved components: real directories, no links
	missingAt := -1  // len(cur) when a component turned out not to exist
	hops := 0
	join := func(parts []string) string { return "/" + strings.Join(parts, "/") }
	for len(queue) > 0 {
		part := queue[0]
		queue = queue[1:]
		switch part {
		case ".":
			continue
		case "..":
			if len(cur) > 0 {
				cur = cur[:len(cur)-1]
			}
			if missingAt >= 0 && len(cur) < missingAt {
				missingAt = -1 // back on paths that exist
			}
			continue
		}
		if missingAt >= 0 {
			cur = append(cur, part)
			continue
		}
		next := join(append(cur[:len(cur):len(cur)], part))
		fi, err := os.Lstat(next)
		if err != nil {
			// Does not exist (or cannot be examined): what follows is
			// taken as written, under what resolved so far.
			missingAt = len(cur)
			cur = append(cur, part)
			continue
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			cur = append(cur, part)
			continue
		}
		hops++
		if hops > maxLinkHops {
			return filepath.Clean(p), false
		}
		target, err := os.Readlink(next)
		if err != nil {
			return filepath.Clean(p), false
		}
		if filepath.IsAbs(target) {
			cur = nil
		}
		// A relative target is walked from the link's directory, which
		// is cur as it stands.
		queue = append(splitPath(target), queue...)
	}
	return join(cur), true
}

func splitPath(p string) []string {
	var out []string
	for _, s := range strings.Split(p, string(filepath.Separator)) {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
