package settings

import (
	"context"
	"crypto/sha256"
	"os"
	"time"
)

// Watching settings files. Claude Code watches its settings files and
// applies a change mid-session (its settings change detector, and
// applySettingsChange: permission rules are re-read from disk and replace
// the ones that came from settings files, keeping session and command-line
// rules). kiln polls instead of using filesystem events: a handful of small
// files, read once a second, with no dependency and the same behaviour on
// every platform.
//
// A change is reported only once a file's content has held still for one
// more poll, as Claude Code waits for a write to finish before reading,
// so a save caught halfway, or a delete followed by a re-create, is not
// read as the new settings.

// DefaultWatchInterval is how often WatchFiles looks at the files.
const DefaultWatchInterval = time.Second

// fingerprint is a file's state: its content's hash, or absent.
type fingerprint struct {
	exists bool
	sum    [sha256.Size]byte
}

func fingerprintOf(path string) fingerprint {
	data, err := os.ReadFile(path)
	if err != nil {
		// Absent and unreadable look the same here; LoadSettings tells
		// them apart when it reads the file.
		return fingerprint{}
	}
	return fingerprint{exists: true, sum: sha256.Sum256(data)}
}

// WatchFiles polls files every interval until ctx is done, and calls
// onChange with the files whose content changed and then held still for
// one more poll. The state when it starts is the baseline: nothing is
// reported for it. onChange runs on WatchFiles' goroutine; a slow one
// delays the next poll, never overlaps it.
func WatchFiles(ctx context.Context, files []string, interval time.Duration, onChange func(changed []string)) {
	if interval <= 0 {
		interval = DefaultWatchInterval
	}
	w := newWatchState(files)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if changed := w.poll(); len(changed) > 0 && ctx.Err() == nil {
			onChange(changed)
		}
	}
}

// watchState is WatchFiles' memory between polls.
type watchState struct {
	files   []string
	applied map[string]fingerprint // what was last reported (or the start)
	pending map[string]fingerprint // a change waiting to hold still
}

func newWatchState(files []string) *watchState {
	w := &watchState{files: files, applied: map[string]fingerprint{}, pending: map[string]fingerprint{}}
	for _, f := range files {
		w.applied[f] = fingerprintOf(f)
	}
	return w
}

// poll looks at every file once and returns those whose new content was
// also what the previous poll saw.
func (w *watchState) poll() []string {
	var changed []string
	for _, f := range w.files {
		now := fingerprintOf(f)
		if now == w.applied[f] {
			delete(w.pending, f)
			continue
		}
		if prev, ok := w.pending[f]; ok && prev == now {
			w.applied[f] = now
			delete(w.pending, f)
			changed = append(changed, f)
			continue
		}
		w.pending[f] = now
	}
	return changed
}
