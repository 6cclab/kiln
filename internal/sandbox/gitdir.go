package sandbox

import (
	"os"
	"path/filepath"
	"strings"
)

// Git directories inside the writable roots hold what git executes or
// follows later, outside the sandbox: hooks, config (core.fsmonitor,
// filters), commondir and gitdir (which point git at another directory's
// hooks and config). The sandbox lets a command write only what git itself
// writes there.

// gitWritableDirs and gitWritableFiles are what git writes inside a git
// directory (and inside the git directories nested in it under modules/
// and worktrees/): object and ref storage, logs, the index, the HEAD files
// and messages of commits, merges, rebases and fetches, and their lock
// files.
//
// Seatbelt's regular expressions are a small dialect (a character class
// holding "." or "-" fails to parse, a starred group is refused), so each
// entry is its own plain expression.
var (
	gitWritableDirs  = []string{"objects", "refs", "logs", "rebase-merge", "rebase-apply", "sequencer", "rr-cache", "lfs"}
	gitWritableFiles = []string{`index`, `index\.stash\.[0-9]+`, `next-index-[0-9]+`, `sharedindex\.[0-9a-f]+`,
		"HEAD", "ORIG_HEAD", "FETCH_HEAD", "MERGE_HEAD", "MERGE_MSG", "MERGE_MODE", "MERGE_RR",
		"CHERRY_PICK_HEAD", "REVERT_HEAD", "REBASE_HEAD", "AUTO_MERGE", "COMMIT_EDITMSG", "SQUASH_MSG",
		"TAG_EDITMSG", "BISECT_LOG", "BISECT_START", "BISECT_TERMS", "BISECT_EXPECTED_REV",
		"BISECT_ANCESTORS_OK", "BISECT_NAMES", "BISECT_RUN", "BISECT_HEAD",
		"packed-refs", "shallow", `gc\.log`, `gc\.pid`}
)

// gitDirRules renders a git directory for Seatbelt as deny-by-default with
// the writable entries re-opened. New directories may be made under
// modules/ and worktrees/ (where git creates a submodule's or a worktree's
// git directory), but only the writable files can go in them, so cloning
// a submodule or adding a worktree, which write a config or gitdir file,
// does not work inside the sandbox.
func gitDirRules(gitdir string) []string {
	g := realPath(gitdir)
	if checkPath(g) != nil {
		return []string{`(deny file-write* (regex #"^/"))`}
	}
	q := regexQuote(g)
	nested := `(/(modules|worktrees)/.+)?`
	var allow []string
	for _, d := range gitWritableDirs {
		allow = append(allow, `(regex #"^`+q+nested+`/`+d+`(/.*)?$")`)
	}
	for _, f := range gitWritableFiles {
		allow = append(allow, `(regex #"^`+q+nested+`/`+f+`$")`, `(regex #"^`+q+nested+`/`+f+`\.lock$")`)
	}
	return []string{
		`(deny file-write* (subpath "` + g + `"))`,
		`(allow file-write* ` + strings.Join(allow, " ") + `)`,
		`(allow file-write-create (require-all (vnode-type DIRECTORY) (regex #"^` + q + `/(modules|worktrees)/")))`,
	}
}

// gitSnapshot records which gitSensitive entries exist in the git
// directories of a plan, before a Linux command runs.
type gitSnapshot map[string]bool

func snapshotGitDirs(dirs []string) gitSnapshot {
	s := gitSnapshot{}
	for _, d := range dirs {
		for _, g := range nestedGitDirs(d) {
			for _, e := range gitSensitive {
				p := filepath.Join(g, e)
				if _, err := os.Lstat(p); err == nil {
					s[p] = true
				}
			}
		}
	}
	return s
}

// sweepGitDirs removes the gitSensitive entries that appeared in the git
// directories while a Linux command ran (the existing ones were bound
// read-only; a new one cannot be, as bubblewrap binds only what exists).
// Claude Code's Linux sandbox does the same for a top-level HEAD, objects
// or refs that appears during a command.
func sweepGitDirs(dirs []string, before gitSnapshot) []string {
	var removed []string
	for p := range snapshotGitDirs(dirs) {
		if !before[p] {
			if os.RemoveAll(p) == nil {
				removed = append(removed, p)
			}
		}
	}
	return removed
}
