package execenv

import (
	"crypto/sha256"
	"sync"
)

// StaleFileMessage is Claude Code's refusal for an edit or write to a file
// that changed after the session last read or wrote it (2.1.319, verbatim).
const StaleFileMessage = "File has been modified since read, either by the user or by a linter. Read it again before attempting to write it."

// fileStamp is what the session last saw of a file: the hash of its whole
// content, or for a partial read (offset/limit), its size and mtime.
type fileStamp struct {
	hash    [sha256.Size]byte
	whole   bool
	size    int64
	mtimeMs int64
}

type fileStamps struct {
	mu sync.Mutex
	m  map[string]fileStamp
}

// stampsInit guards creating an Env's fileStamps on first use.
var stampsInit sync.Mutex

func (e *Env) stamps() *fileStamps {
	stampsInit.Lock()
	defer stampsInit.Unlock()
	if e.fileStamps == nil {
		e.fileStamps = &fileStamps{m: map[string]fileStamp{}}
	}
	return e.fileStamps
}

// NoteFileContent records that the session saw path's whole content: a
// whole-file read, or what an edit or write just put there.
func (e *Env) NoteFileContent(path string, content []byte) {
	s := e.stamps()
	s.mu.Lock()
	s.m[e.AbsolutePath(path)] = fileStamp{hash: sha256.Sum256(content), whole: true}
	s.mu.Unlock()
}

// NoteFilePart records that the session read part of path (offset/limit),
// so only its size and mtime can tell whether it changed since.
func (e *Env) NoteFilePart(path string, info FileInfo) {
	s := e.stamps()
	s.mu.Lock()
	s.m[e.AbsolutePath(path)] = fileStamp{size: info.Size, mtimeMs: info.MtimeMs}
	s.mu.Unlock()
}

// ChangedSinceSeen reports whether path, whose content is now current,
// changed after the session last read or wrote it. A file the session
// never read or wrote has nothing to compare against, so it reports false.
func (e *Env) ChangedSinceSeen(path string, current []byte) bool {
	abs := e.AbsolutePath(path)
	s := e.stamps()
	s.mu.Lock()
	st, ok := s.m[abs]
	s.mu.Unlock()
	if !ok {
		return false
	}
	if st.whole {
		return sha256.Sum256(current) != st.hash
	}
	info, err := e.Stat(abs)
	return err != nil || info.Size != st.size || info.MtimeMs != st.mtimeMs
}
