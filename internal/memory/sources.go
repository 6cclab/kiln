package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/andrepato/harness/internal/diag"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Source binds an observation to the exact contents of a project file.
type Source struct {
	Root   string `json:"root"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func sourceDigest(root, path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("memory source is outside project")
	}
	f, err := os.Open(resolved)
	if err != nil {
		return "", err
	}
	defer diag.Close(f)
	info, err := f.Stat()
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
		digest, err := sourceDigest(canonical, path)
		if err != nil {
			return nil, err
		}
		out = append(out, Source{Root: canonical, Path: path, SHA256: digest})
	}
	return out, nil
}

// SourcesCurrent rejects missing, changed, or redirected source files.
func SourcesCurrent(sources []Source) bool {
	for _, s := range sources {
		digest, err := sourceDigest(s.Root, s.Path)
		if err != nil || digest != s.SHA256 {
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
		rebound = append(rebound, Source{Root: canonical, Path: filepath.Join(canonical, relative), SHA256: source.SHA256})
	}
	return SourcesCurrent(rebound)
}
