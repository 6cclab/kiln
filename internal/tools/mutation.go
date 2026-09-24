package tools

import "sync"

// fileLocks serializes write and edit tool calls against the same path,
// mirroring file-mutation-queue.js's withFileMutationQueue (which
// chains a promise queue per canonical path so two concurrent tool calls
// targeting the same file cannot interleave their read-modify-write).
// This port uses one *sync.Mutex per absolute path instead of a promise
// chain; the effect — mutations to the same file never run concurrently —
// is the same. Deviation: pi keys the queue off the canonical
// (symlink-resolved) path so two different-looking paths that alias the
// same file still serialize; this port keys off the resolved absolute
// path only, so two symlinks to the same target are not detected as the
// same file.
var (
	fileLocksMu sync.Mutex
	fileLocks   = map[string]*sync.Mutex{}
)

func lockForPath(path string) *sync.Mutex {
	fileLocksMu.Lock()
	defer fileLocksMu.Unlock()
	m, ok := fileLocks[path]
	if !ok {
		m = &sync.Mutex{}
		fileLocks[path] = m
	}
	return m
}

// withFileMutationLock runs fn while holding the lock for path.
func withFileMutationLock[T any](path string, fn func() (T, error)) (T, error) {
	m := lockForPath(path)
	m.Lock()
	defer m.Unlock()
	return fn()
}
