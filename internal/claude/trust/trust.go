// Package trust tracks which working directories the person has told the
// harness to trust, mirroring Claude Code's folder-trust prompt. The store
// is a flat JSON list of absolute paths at ~/.harness/trusted.json; a
// trusted ancestor directory trusts everything under it, so trusting a repo
// root also trusts any worktree or subdirectory inside it.
package trust

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// storeFileName is the file under the store's root directory. The root
// itself is ~/.harness (HomeDir()/.harness), matching this package's own
// namespace rather than Claude Code's ~/.claude (this is the harness's own
// trust store, not a read of Claude Code's).
const (
	storeDirName  = ".harness"
	storeFileName = "trusted.json"
)

// Store is a JSON-backed set of trusted absolute directories at path.
type Store struct {
	path string
}

// NewStore opens the default store at ~/.harness/trusted.json. It does not
// touch disk until Trust or IsTrusted is called.
func NewStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Store{path: filepath.Join(home, storeDirName, storeFileName)}, nil
}

// NewStoreAt opens a store at an explicit path, for tests.
func NewStoreAt(path string) *Store {
	return &Store{path: path}
}

// load reads the store's trusted paths. A missing file is an empty store,
// not an error.
func (s *Store) load() ([]string, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var paths []string
	if err := json.Unmarshal(data, &paths); err != nil {
		return nil, err
	}
	return paths, nil
}

// save writes paths atomically: a temp file in the same directory, then a
// rename, so a crash mid-write never leaves a truncated trusted.json. Mode
// 0600 since this is a private, unshared record of what the user has
// trusted.
func (s *Store) save(paths []string) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(paths, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".trusted-*.json.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpPath, s.path)
}

// normalize resolves cwd to a clean absolute path so comparisons are not
// fooled by trailing slashes, "." segments or relative paths. It does not
// resolve symlinks (no filepath.EvalSymlinks): a symlinked path is treated
// as its own identity, matching what the caller (a cwd string, not a
// resolved inode) actually has.
func normalize(cwd string) (string, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

// IsTrusted reports whether cwd is trusted: either it was trusted directly,
// or one of its ancestor directories was.
func (s *Store) IsTrusted(cwd string) bool {
	target, err := normalize(cwd)
	if err != nil {
		return false
	}
	paths, err := s.load()
	if err != nil {
		return false
	}
	for _, p := range paths {
		if isAncestorOrSelf(p, target) {
			return true
		}
	}
	return false
}

// isAncestorOrSelf reports whether target is ancestor or ancestor itself
// (equal), or a descendant of ancestor.
func isAncestorOrSelf(ancestor, target string) bool {
	if ancestor == target {
		return true
	}
	prefix := ancestor
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(target, prefix)
}

// Trust records cwd as trusted. It is idempotent: trusting an
// already-trusted path (directly or via an ancestor) does not append a
// duplicate entry.
func (s *Store) Trust(cwd string) error {
	target, err := normalize(cwd)
	if err != nil {
		return err
	}
	paths, err := s.load()
	if err != nil {
		return err
	}
	for _, p := range paths {
		if isAncestorOrSelf(p, target) {
			return nil
		}
	}
	paths = append(paths, target)
	return s.save(paths)
}
