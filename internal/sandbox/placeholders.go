package sandbox

import (
	"os"
	"path/filepath"
)

// placeholders holds the protected paths that do not exist yet, for the
// Linux sandbox. bubblewrap can only bind a path that exists, so a
// missing .claude/settings.json in the project could otherwise be created
// by a sandboxed command and change what kiln loads next time. As Claude
// Code does on Linux, kiln creates an empty read-only file at the first
// missing component of each such path inside a writable root (".claude"
// itself when the directory is missing), binds it read-only into the
// sandbox, and removes it after the command when it is still an empty
// file. Concurrent commands share a placeholder; the last one out removes
// it.
func (m *Manager) placeholders(p Plan) (held []string, release func(), err error) {
	var want []string
	for _, r := range p.DenyWrite {
		if r.glob() {
			continue
		}
		path := realPath(r.Path)
		if exists(path) || !underAny(path, p.WriteRoots) {
			continue
		}
		// Inside a git directory an empty file is not inert (an empty
		// commondir points git at the wrong place); sweepGitDirs removes
		// what appears there instead.
		if underAny(path, p.GitDirs) {
			continue
		}
		// The first missing component, or the file in the way of one (a
		// placeholder another command already holds).
		first := path
		for {
			parent := filepath.Dir(first)
			if parent == first || isDir(parent) {
				break
			}
			first = parent
			if exists(parent) {
				break
			}
		}
		if underAny(first, p.WriteRoots) && !isRootItself(first, p.WriteRoots) {
			want = append(want, first)
		}
	}
	want = dedupe(want)

	m.mu.Lock()
	defer m.mu.Unlock()
	var created []string
	for _, path := range want {
		if m.held[path] == 0 {
			f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o444)
			if err != nil {
				if os.IsExist(err) {
					continue // it appeared meanwhile: the regular bind covers it
				}
				m.releaseLocked(created)
				return nil, func() {}, err
			}
			f.Close()
		}
		m.held[path]++
		created = append(created, path)
	}
	return created, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.releaseLocked(created)
	}, nil
}

func (m *Manager) releaseLocked(paths []string) {
	for _, path := range paths {
		m.held[path]--
		if m.held[path] > 0 {
			continue
		}
		delete(m.held, path)
		if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() && fi.Size() == 0 {
			_ = os.Remove(path)
		}
	}
}

func isRootItself(p string, roots []string) bool {
	for _, r := range roots {
		if realPath(r) == p {
			return true
		}
	}
	return false
}
