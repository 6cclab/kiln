package execenv

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FileKind is the kind of filesystem object addressed by Env, mirroring
// pi's FileKind ("file" | "directory" | "symlink"). Symlinks are not
// followed automatically.
type FileKind string

const (
	KindFile      FileKind = "file"
	KindDirectory FileKind = "directory"
	KindSymlink   FileKind = "symlink"
)

// FileInfo is metadata for one filesystem object, mirroring pi's FileInfo.
type FileInfo struct {
	Name    string
	Path    string
	Kind    FileKind
	Size    int64
	MtimeMs int64
}

// Env is the filesystem and shell execution environment used by tools. It
// mirrors pi's NodeExecutionEnv: relative paths resolve against Cwd, "~"
// expands to the user's home directory, and file operations never panic —
// failures come back as an error.
type Env struct {
	// Cwd is the working directory relative paths resolve against.
	Cwd string
	// ShellPath overrides the shell used by Exec. Empty selects pi's
	// default: /bin/bash if present, else the first "bash" on PATH, else
	// "sh".
	ShellPath string
}

// New builds an Env rooted at cwd.
func New(cwd string) *Env {
	return &Env{Cwd: cwd}
}

// AbsolutePath returns an absolute, syntactically normalized path without
// requiring it to exist and without resolving symlinks, mirroring
// resolvePath in nodejs.js: "~" and "~/..." expand to the home directory,
// relative paths resolve against Cwd, everything else is passed through
// filepath.Clean.
func (e *Env) AbsolutePath(path string) string {
	return resolvePath(e.Cwd, path)
}

func resolvePath(cwd, path string) string {
	normalized := path
	if normalized == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			normalized = home
		}
	} else if strings.HasPrefix(normalized, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			normalized = filepath.Join(home, normalized[2:])
		}
	} else if strings.HasPrefix(normalized, "file://") {
		if u, err := parseFileURL(normalized); err == nil {
			normalized = u
		}
	}
	if filepath.IsAbs(normalized) {
		return filepath.Clean(normalized)
	}
	return filepath.Clean(filepath.Join(cwd, normalized))
}

func parseFileURL(raw string) (string, error) {
	// Minimal file:// URL support: file:///abs/path -> /abs/path. Malformed
	// URLs are kept as ordinary paths by the caller, matching nodejs.js's
	// fallback behavior.
	const prefix = "file://"
	if !strings.HasPrefix(raw, prefix) {
		return raw, nil
	}
	rest := raw[len(prefix):]
	if rest == "" {
		return "", os.ErrInvalid
	}
	return rest, nil
}

// ReadFile reads a whole file as bytes. path may be relative to Cwd.
func (e *Env) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(e.AbsolutePath(path))
}

// WriteFile creates or overwrites a file, creating parent directories.
func (e *Env) WriteFile(path string, content []byte) error {
	resolved := e.AbsolutePath(path)
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return err
	}
	return os.WriteFile(resolved, content, 0o644)
}

// ListDir lists the direct children of a directory without following
// symlinks, sorted by name for deterministic output.
func (e *Env) ListDir(path string) ([]FileInfo, error) {
	resolved := e.AbsolutePath(path)
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return nil, err
	}
	infos := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		entryPath := filepath.Join(resolved, entry.Name())
		info, err := statNoFollow(entryPath)
		if err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos, nil
}

// Stat returns metadata for path without following symlinks, mirroring
// pi's fileInfo (which uses lstat).
func (e *Env) Stat(path string) (FileInfo, error) {
	return statNoFollow(e.AbsolutePath(path))
}

func statNoFollow(path string) (FileInfo, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return FileInfo{}, err
	}
	kind := KindFile
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		kind = KindSymlink
	case fi.IsDir():
		kind = KindDirectory
	}
	return FileInfo{
		Name:    filepath.Base(path),
		Path:    path,
		Kind:    kind,
		Size:    fi.Size(),
		MtimeMs: fi.ModTime().UnixMilli(),
	}, nil
}

// Exists reports whether path exists. Errors other than "not found" (for
// example permission failures) are returned as an error, mirroring pi's
// exists().
func (e *Env) Exists(path string) (bool, error) {
	_, err := e.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// RemoveOptions controls Remove.
type RemoveOptions struct {
	// Recursive removes directories and their contents.
	Recursive bool
	// Force ignores a missing path instead of returning an error.
	Force bool
}

// Remove removes a file or directory, mirroring pi's remove().
func (e *Env) Remove(path string, opts RemoveOptions) error {
	resolved := e.AbsolutePath(path)
	var err error
	if opts.Recursive {
		err = os.RemoveAll(resolved)
	} else {
		err = os.Remove(resolved)
	}
	if opts.Force && os.IsNotExist(err) {
		return nil
	}
	return err
}

// CreateDir creates a directory. recursive defaults to true, mirroring
// pi's createDir().
func (e *Env) CreateDir(path string, recursive bool) error {
	resolved := e.AbsolutePath(path)
	if recursive {
		return os.MkdirAll(resolved, 0o755)
	}
	return os.Mkdir(resolved, 0o755)
}

// CanonicalPath resolves symlinks, mirroring pi's canonicalPath().
func (e *Env) CanonicalPath(path string) (string, error) {
	return filepath.EvalSymlinks(e.AbsolutePath(path))
}
