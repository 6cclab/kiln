package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/andrepato/harness/internal/diag"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Source kinds. The empty kind is a regular file, which is what every row
// written before the kinds existed holds, so old rows need no migration.
const (
	SourceFile   = ""
	SourceDir    = "dir"
	SourceAbsent = "absent"
)

// Source binds an observation to the exact contents of a project file, to the
// recursive membership and contents of a directory (Kind "dir", SHA256 is the
// tree digest), or to the continued absence of a path (Kind "absent").
type Source struct {
	Root      string `json:"root"`
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	Mode      uint32 `json:"mode,omitempty"`
	ModeKnown bool   `json:"modeKnown,omitempty"`
	Kind      string `json:"kind,omitempty"`
}

const (
	maxTreeEntries = 512
	maxTreeBytes   = 16 << 20
)

// TreeEntry is one directory member for TreeDigest. Path is relative to the
// digested directory; the directory itself is {Kind: "d", Path: "."}.
type TreeEntry struct {
	Kind   string // "d" or "f"
	Path   string
	Mode   uint32
	SHA256 string // files only
}

// TreeDigest hashes entries in sorted order, so a snapshot taken from disk and
// one rebuilt from a verification proof agree.
func TreeDigest(entries []TreeEntry) string {
	sorted := append([]TreeEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	h := sha256.New()
	for _, e := range sorted {
		fmt.Fprintf(h, "%s\x00%s\x00%o\x00%s\n", e.Kind, e.Path, e.Mode, e.SHA256)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// dirTreeDigest walks dir without following symlinks and skips .git, as
// verification snapshots do. Anything but directories and regular files at most
// 1 MiB, more than 512 entries, or more than 16 MiB of content is an error.
// allowRead, when set, receives each entry's project-relative path.
func dirTreeDigest(canonical, dir string, allowRead func(string) bool) (string, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("memory directory source is not a directory")
	}
	entries := []TreeEntry{{Kind: "d", Path: ".", Mode: uint32(info.Mode().Perm())}}
	var total int64
	var walk func(string, string) error
	walk = func(abs, within string) error {
		names, err := os.ReadDir(abs)
		if err != nil {
			return err
		}
		for _, name := range names {
			if name.Name() == ".git" {
				continue
			}
			if len(entries) >= maxTreeEntries {
				return fmt.Errorf("memory directory source exceeds entry limit")
			}
			child := filepath.Join(abs, name.Name())
			childWithin := filepath.ToSlash(filepath.Join(within, name.Name()))
			if allowRead != nil {
				rel, err := filepath.Rel(canonical, child)
				if err != nil || !allowRead(rel) {
					return ErrSourceReadDenied
				}
			}
			info, err := os.Lstat(child)
			if err != nil {
				return err
			}
			switch {
			case info.IsDir():
				entries = append(entries, TreeEntry{Kind: "d", Path: childWithin, Mode: uint32(info.Mode().Perm())})
				if err := walk(child, childWithin); err != nil {
					return err
				}
			case info.Mode().IsRegular() && info.Size() <= 1<<20:
				total += info.Size()
				if total > maxTreeBytes {
					return fmt.Errorf("memory directory source exceeds byte limit")
				}
				digest, err := sourceDigest(canonical, child)
				if err != nil {
					return err
				}
				entries = append(entries, TreeEntry{Kind: "f", Path: childWithin, Mode: uint32(info.Mode().Perm()), SHA256: digest})
			default:
				return fmt.Errorf("memory directory source holds a symlink, special file or file over 1 MiB")
			}
		}
		return nil
	}
	if err := walk(dir, ""); err != nil {
		return "", err
	}
	return TreeDigest(entries), nil
}

// SourceSpec names one project path and the kind of claim made about it.
type SourceSpec struct {
	Kind string
	Path string
}

// CaptureSourceSpecs snapshots up to eight files, directories and absent paths
// inside a canonical project root, rechecking Read around every capture.
func CaptureSourceSpecs(root string, specs []SourceSpec, allowRead func(string) bool) ([]Source, error) {
	if allowRead == nil {
		return nil, ErrSourceReadDenied
	}
	if len(specs) > 8 {
		return nil, fmt.Errorf("at most eight memory sources are allowed")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	out := make([]Source, 0, len(specs))
	for _, spec := range specs {
		path := spec.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(canonical, path)
		}
		rel, err := filepath.Rel(canonical, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("memory source is outside project")
		}
		if !allowRead(rel) {
			return nil, ErrSourceReadDenied
		}
		var src Source
		switch spec.Kind {
		case SourceFile:
			one, err := captureSources(canonical, []string{path}, allowRead)
			if err != nil {
				return nil, err
			}
			src = one[0]
		case SourceDir:
			if resolved, err := filepath.EvalSymlinks(path); err != nil || resolved != path {
				return nil, fmt.Errorf("memory directory source must not traverse symlinks")
			}
			info, err := os.Lstat(path)
			if err != nil {
				return nil, err
			}
			digest, err := dirTreeDigest(canonical, path, allowRead)
			if err != nil {
				return nil, err
			}
			src = Source{Root: canonical, Path: path, SHA256: digest, Mode: uint32(info.Mode().Perm()), ModeKnown: true, Kind: SourceDir}
		case SourceAbsent:
			if rel == "." || !absentPathCurrent(path) {
				return nil, fmt.Errorf("memory absent source exists")
			}
			src = Source{Root: canonical, Path: path, Kind: SourceAbsent}
		default:
			return nil, fmt.Errorf("unknown memory source kind")
		}
		out = append(out, src)
	}
	for _, source := range out {
		rel, err := filepath.Rel(canonical, source.Path)
		if err != nil || !allowRead(rel) {
			return nil, ErrSourceReadDenied
		}
	}
	return out, nil
}

func absentPathCurrent(path string) bool {
	_, err := os.Lstat(path)
	return os.IsNotExist(err)
}

func sourceCurrent(s Source) bool {
	switch s.Kind {
	case SourceAbsent:
		return absentPathCurrent(s.Path)
	case SourceDir:
		canonical, err := filepath.EvalSymlinks(s.Root)
		if err != nil {
			return false
		}
		if resolved, err := filepath.EvalSymlinks(s.Path); err != nil || resolved != s.Path {
			return false
		}
		digest, err := dirTreeDigest(canonical, s.Path, nil)
		if err != nil || digest != s.SHA256 {
			return false
		}
		info, err := os.Lstat(s.Path)
		return err == nil && (!s.ModeKnown || uint32(info.Mode().Perm()) == s.Mode)
	case SourceFile:
		digest, err := sourceDigest(s.Root, s.Path)
		if err != nil || digest != s.SHA256 {
			return false
		}
		if s.ModeKnown {
			info, err := os.Stat(s.Path)
			if err != nil || uint32(info.Mode().Perm()) != s.Mode {
				return false
			}
		}
		return true
	}
	return false
}

var ErrSourceReadDenied = errors.New("current Read permission denied for memory source")

func sourceDigest(root, path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("memory source is outside project")
	}
	// Reject special files before opening them. Open nonblocking as well:
	// a regular file can be replaced by a FIFO between Stat and OpenFile.
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return "", fmt.Errorf("memory sources must be regular files at most 1 MiB")
	}
	f, err := os.OpenFile(resolved, os.O_RDONLY|openNonblock, 0)
	if err != nil {
		return "", err
	}
	defer diag.Close(f)
	info, err = f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return "", fmt.Errorf("memory sources must be regular files at most 1 MiB")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return "", err
	}
	if n > 1<<20 {
		return "", fmt.Errorf("memory source grew beyond limit")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// CaptureSources snapshots up to eight files inside a canonical project root.
func CaptureSources(root string, paths []string) ([]Source, error) {
	return captureSources(root, paths, nil)
}

// CaptureSourcesWithRead rechecks the current Read decision around each file
// snapshot. A nil callback fails closed for automatic extraction.
func CaptureSourcesWithRead(root string, paths []string, allowRead func(string) bool) ([]Source, error) {
	if allowRead == nil {
		return nil, ErrSourceReadDenied
	}
	return captureSources(root, paths, allowRead)
}

func captureSources(root string, paths []string, allowRead func(string) bool) ([]Source, error) {
	if len(paths) > 8 {
		return nil, fmt.Errorf("at most eight memory source files are allowed")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	out := make([]Source, 0, len(paths))
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			path = filepath.Join(canonical, path)
		}
		rel, err := filepath.Rel(canonical, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("memory source is outside project")
		}
		if allowRead != nil && !allowRead(rel) {
			return nil, ErrSourceReadDenied
		}
		digest, err := sourceDigest(canonical, path)
		if err != nil {
			return nil, err
		}
		if allowRead != nil && !allowRead(rel) {
			return nil, ErrSourceReadDenied
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		out = append(out, Source{Root: canonical, Path: path, SHA256: digest, Mode: uint32(info.Mode().Perm()), ModeKnown: true})
	}
	if allowRead != nil {
		for _, source := range out {
			rel, err := filepath.Rel(canonical, source.Path)
			if err != nil || !allowRead(rel) {
				return nil, ErrSourceReadDenied
			}
		}
	}
	return out, nil
}

// SourcesCurrent rejects missing, changed, or redirected source files.
func SourcesCurrent(sources []Source) bool {
	for _, s := range sources {
		if !sourceCurrent(s) {
			return false
		}
	}
	return true
}

// SourcesCurrentAt checks repository-relative sources against the current
// worktree, rather than a different worktree that originally recorded them.
func SourcesCurrentAt(sources []Source, root string) bool {
	if root == "" {
		return SourcesCurrent(sources)
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	rebound := make([]Source, 0, len(sources))
	for _, source := range sources {
		relative, err := filepath.Rel(source.Root, source.Path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return false
		}
		rebound = append(rebound, Source{Root: canonical, Path: filepath.Join(canonical, relative), SHA256: source.SHA256, Mode: source.Mode, ModeKnown: source.ModeKnown, Kind: source.Kind})
	}
	return SourcesCurrent(rebound)
}
