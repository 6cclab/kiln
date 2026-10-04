// Package gitfiles answers "is this a git repository, and on which
// branch" by reading .git directly, without running git. git reads the
// repository's own config, and some of it names programs git runs (an
// fsmonitor, a clean filter .gitattributes selects); before the person
// has trusted a folder kiln must not let the folder run anything, so what
// it needs from git before trust it reads from the files instead.
package gitfiles

import (
	"os"
	"path/filepath"
	"strings"
)

// Repo is the repository a directory is in.
type Repo struct {
	// WorkTree is the directory holding .git.
	WorkTree string
	// GitDir is the repository directory for this work tree: WorkTree/.git,
	// or for a linked worktree or submodule the directory its .git file
	// names.
	GitDir string
	// CommonDir is the directory every worktree of the repository shares
	// (git rev-parse --git-common-dir): GitDir, or the one GitDir's
	// commondir file names.
	CommonDir string
}

// Find returns the repository dir is in, looking at dir and each parent
// for a .git directory or a .git file ("gitdir: <path>"). ok is false
// outside any repository, or when .git is there but cannot be read.
func Find(dir string) (Repo, bool) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return Repo{}, false
	}
	for {
		dotGit := filepath.Join(dir, ".git")
		if fi, err := os.Stat(dotGit); err == nil {
			gitDir := dotGit
			if !fi.IsDir() {
				data, err := os.ReadFile(dotGit)
				if err != nil {
					return Repo{}, false
				}
				p, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
				if !ok {
					return Repo{}, false
				}
				gitDir = absUnder(dir, strings.TrimSpace(p))
			}
			if _, err := os.Stat(filepath.Join(gitDir, "HEAD")); err != nil {
				return Repo{}, false
			}
			common := gitDir
			if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
				common = absUnder(gitDir, strings.TrimSpace(string(data)))
			}
			return Repo{WorkTree: dir, GitDir: gitDir, CommonDir: common}, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return Repo{}, false
		}
		dir = parent
	}
}

func absUnder(base, p string) string {
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	return filepath.Clean(p)
}

// Branch is HEAD's branch name, "HEAD" when HEAD is detached (as git
// rev-parse --abbrev-ref HEAD prints it), and born reports whether the
// branch has a commit yet. ok is false when HEAD cannot be read.
func (r Repo) Branch() (branch string, born, ok bool) {
	data, err := os.ReadFile(filepath.Join(r.GitDir, "HEAD"))
	if err != nil {
		return "", false, false
	}
	head := strings.TrimSpace(string(data))
	ref, isRef := strings.CutPrefix(head, "ref:")
	if !isRef {
		return "HEAD", head != "", true
	}
	ref = strings.TrimSpace(ref)
	branch = strings.TrimPrefix(ref, "refs/heads/")
	return branch, r.refExists(ref), true
}

// refExists reports a loose or packed ref.
func (r Repo) refExists(ref string) bool {
	for _, dir := range []string{r.GitDir, r.CommonDir} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(ref))); err == nil {
			return true
		}
	}
	data, err := os.ReadFile(filepath.Join(r.CommonDir, "packed-refs"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if _, name, ok := strings.Cut(strings.TrimSpace(line), " "); ok && name == ref {
			return true
		}
	}
	return false
}
