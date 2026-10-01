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
// at a time, the way the kernel walks it on open. Unlike
// filepath.EvalSymlinks it follows a dangling link (a link whose target
// does not exist yet): writing through "dangle -> ~/.ssh/authorized_keys"
// creates the target, so the target is the path that matters. Components
// past the first one that does not exist are kept as written. ok is false
// when the path cannot be resolved (a symlink loop or an unreadable link);
// p is then returned cleaned.
func RealPath(p string) (string, bool) {
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) {
		return p, false
	}
	parts := splitPath(p)
	cur := "/"
	hops := 0
	for i := 0; i < len(parts); i++ {
		next := filepath.Join(cur, parts[i])
		fi, err := os.Lstat(next)
		if err != nil {
			// Does not exist (or cannot be examined): the rest is
			// literal, under what resolved so far.
			return filepath.Join(append([]string{next}, parts[i+1:]...)...), true
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			cur = next
			continue
		}
		hops++
		if hops > maxLinkHops {
			return p, false
		}
		target, err := os.Readlink(next)
		if err != nil {
			return p, false
		}
		if !filepath.IsAbs(target) {
			// cur has no symlinks left in it, so ".." in the target is
			// lexical against a real directory.
			target = filepath.Join(cur, target)
		}
		parts = append(splitPath(filepath.Clean(target)), parts[i+1:]...)
		cur, i = "/", -1
	}
	return cur, true
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
