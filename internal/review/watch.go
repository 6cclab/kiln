package review

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// SourceOutside marks a change found between tool calls: made by hand or
// by a background command while kiln was running.
const SourceOutside = "outside"

// maxWatchedBytes bounds one file's content kept for a revert; a bigger
// file is still logged, without its previous content.
const maxWatchedBytes = maxBeforeBytes

// watchedFile is what the watch last saw of one file under the skills
// directory.
type watchedFile struct {
	size    int64
	mtimeNs int64
	hash    string
	content *string // nil when too large to keep
}

// Watch logs every change under the skills directory in the history,
// whatever made it: kiln's write and edit tools, a bash command, a
// subagent, an MCP tool. Sync compares the directory with what the watch
// last saw and records each difference with the file's previous content,
// so /learned can show and revert it. Commands that log their own changes
// (an approval, a revert) call Rebase after them, so nothing is logged
// twice.
type Watch struct {
	mu      sync.Mutex
	dir     string
	h       History
	session string
	last    map[string]watchedFile
}

// NewWatch starts watching dir, taking what is there now as the baseline.
func NewWatch(dir string, h History, session string) *Watch {
	w := &Watch{dir: dir, h: h, session: session}
	w.last = w.scan(nil)
	return w
}

// Sync logs what changed since the last Sync, as made by source, and
// returns how many changes it logged.
func (w *Watch) Sync(source string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.scan(w.last)
	n := 0
	for rel, cur := range now {
		prev, existed := w.last[rel]
		if existed && prev.hash == cur.hash {
			continue
		}
		c := w.change(rel, source)
		c.After = cur.hash
		if existed {
			if prev.content == nil {
				c.BeforeTooLarge = true
			} else {
				c.Before = prev.content
			}
		}
		if _, err := w.h.Record(c); err == nil {
			n++
		}
	}
	for rel, prev := range w.last {
		if _, still := now[rel]; still {
			continue
		}
		c := w.change(rel, source)
		if prev.content == nil {
			c.BeforeTooLarge = true
		} else {
			c.Before = prev.content
		}
		if _, err := w.h.Record(c); err == nil {
			n++
		}
	}
	w.last = now
	return n
}

// Rebase takes the directory as it is now as the baseline without logging
// anything: for changes the history already records whole (an approval,
// an archive or a restore, a revert).
func (w *Watch) Rebase() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.last = w.scan(w.last)
}

func (w *Watch) change(rel, source string) Change {
	skill, file, _ := strings.Cut(rel, "/")
	return Change{Skill: skill, File: file, Op: OpWrite, Source: source, Session: w.session}
}

// scan reads the directory, reusing prev's hash and content for files
// whose size and modification time did not change.
func (w *Watch) scan(prev map[string]watchedFile) map[string]watchedFile {
	out := map[string]watchedFile{}
	_ = filepath.WalkDir(w.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") && path != w.dir {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, ok := underDir(w.dir, path)
		if !ok || !strings.Contains(rel, "/") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		f := watchedFile{size: info.Size(), mtimeNs: info.ModTime().UnixNano()}
		if p, ok := prev[rel]; ok && p.size == f.size && p.mtimeNs == f.mtimeNs {
			out[rel] = p
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		f.hash = hashBytes(data)
		if len(data) <= maxWatchedBytes {
			s := string(data)
			f.content = &s
		}
		out[rel] = f
		return nil
	})
	return out
}
