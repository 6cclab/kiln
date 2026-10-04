// Package gitfiles answers "is this a git repository, and on which
// branch" by reading .git directly, without running git. git reads the
// repository's own config, and some of it names programs git runs (an
// fsmonitor, a clean filter .gitattributes selects); before the person
// has trusted a folder kiln must not let the folder run anything, so what
// it needs from git before trust it reads from the files instead.
//
// The files are the folder's, so each read is guarded: only a regular
// file is opened (a FIFO would block, a device such as /dev/zero never
// ends), the open does not block, and every read is capped. A file that
// fails any of that reads as "not a repository" or "no branch".
package gitfiles

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Caps on what is read: .git, HEAD and commondir hold one short line;
// packed-refs one line per ref, scanned rather than read whole.
const (
	smallFileMax  = 4 << 10
	packedRefsMax = 8 << 20
)

var errNotRegular = errors.New("not a regular file")

// openRegular opens path for reading only if it is a regular file
// (following symlinks, as git does), without blocking, and checks the
// opened file again so a swap between the two checks is caught.
func openRegular(path string) (*os.File, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errNotRegular
	}
	f, err := os.OpenFile(path, os.O_RDONLY|openNonblock, 0)
	if err != nil {
		return nil, err
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, errNotRegular
	}
	return f, nil
}

// readSmall reads a regular file of at most smallFileMax bytes.
func readSmall(path string) ([]byte, error) {
	f, err := openRegular(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, smallFileMax+1))
	if err != nil {
		return nil, err
	}
	if len(data) > smallFileMax {
		return nil, errors.New("too large")
	}
	return data, nil
}

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
// outside any repository, or when the .git found cannot be read safely.
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
				data, err := readSmall(dotGit)
				if err != nil {
					return Repo{}, false
				}
				p, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
				if !ok {
					return Repo{}, false
				}
				gitDir = absUnder(dir, strings.TrimSpace(p))
			}
			if fi, err := os.Stat(filepath.Join(gitDir, "HEAD")); err != nil || !fi.Mode().IsRegular() {
				return Repo{}, false
			}
			common := gitDir
			data, err := readSmall(filepath.Join(gitDir, "commondir"))
			switch {
			case err == nil:
				common = absUnder(gitDir, strings.TrimSpace(string(data)))
			case !errors.Is(err, fs.ErrNotExist):
				return Repo{}, false
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
// branch has a commit yet. ok is false when HEAD cannot be read safely.
func (r Repo) Branch() (branch string, born, ok bool) {
	data, err := readSmall(filepath.Join(r.GitDir, "HEAD"))
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

// refExists reports a loose or packed ref. packed-refs is scanned line by
// line, up to packedRefsMax bytes; a ref past that reads as absent.
func (r Repo) refExists(ref string) bool {
	for _, dir := range []string{r.GitDir, r.CommonDir} {
		if fi, err := os.Stat(filepath.Join(dir, filepath.FromSlash(ref))); err == nil && fi.Mode().IsRegular() {
			return true
		}
	}
	f, err := openRegular(filepath.Join(r.CommonDir, "packed-refs"))
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, packedRefsMax))
	sc.Buffer(make([]byte, 0, 4096), smallFileMax)
	for sc.Scan() {
		if _, name, ok := strings.Cut(strings.TrimSpace(sc.Text()), " "); ok && name == ref {
			return true
		}
	}
	return false
}
