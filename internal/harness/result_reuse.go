package harness

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/andrepato/harness/internal/msg"
)

const (
	resultReuseEntries = 64
	// A result shorter than this costs about as much as the notice replacing it.
	resultReuseMinBytes = 256
)

// resultReuse remembers, per lane, a digest of the last full result of each
// read-only call (read, grep, glob) and the call that produced it. It keeps
// no content. A repeat is replaced by a short pointer only when the tool was
// executed again just now (so the files were re-read and re-checked, and
// permissions and hooks ran as usual), its content digest equals the
// remembered one, and the remembered call's result is still in the transcript
// the model sees (so a compaction cannot leave the pointer dangling).
// Guarded by Lane.reuseMu.
type resultReuse struct {
	entries map[[sha256.Size]byte]reuseEntry
	order   [][sha256.Size]byte
	live    map[string]struct{}
}

type reuseEntry struct {
	callID string
	digest [sha256.Size]byte
}

func reusableTool(name string) bool { return name == "read" || name == "grep" || name == "glob" }

// noteLive records which tool results the next request carries.
func (l *Lane) noteLive(transcript []msg.Message) {
	if !l.h.opts.ReuseUnchangedResults {
		return
	}
	live := make(map[string]struct{})
	for _, m := range transcript {
		if r, ok := m.(msg.ToolResultMessage); ok {
			live[r.ToolCallID] = struct{}{}
		}
	}
	l.reuseMu.Lock()
	if l.reuse == nil {
		l.reuse = &resultReuse{entries: map[[sha256.Size]byte]reuseEntry{}}
	}
	l.reuse.live = live
	l.reuseMu.Unlock()
}

// reuseUnchanged rewrites result in place when it repeats an earlier,
// still-visible result of the same call with identical content.
func (l *Lane) reuseUnchanged(call msg.ToolCall, args map[string]any, result *msg.ToolResultMessage) {
	if !l.h.opts.ReuseUnchangedResults || !reusableTool(call.Name) || result.IsError || len(result.Content) == 0 {
		return
	}
	var text strings.Builder
	for _, block := range result.Content {
		t, ok := block.(msg.TextContent)
		if !ok {
			return
		}
		text.WriteString(t.Text)
		text.WriteByte(0)
	}
	if text.Len() < resultReuseMinBytes {
		return
	}
	argsJSON, err := json.Marshal(args) // map keys are sorted: canonical
	if err != nil {
		return
	}
	key := sha256.Sum256(append([]byte(call.Name+"\x00"), argsJSON...))
	digest := sha256.Sum256([]byte(text.String()))

	l.reuseMu.Lock()
	defer l.reuseMu.Unlock()
	if l.reuse == nil {
		l.reuse = &resultReuse{entries: map[[sha256.Size]byte]reuseEntry{}}
	}
	r := l.reuse
	if prior, ok := r.entries[key]; ok && prior.digest == digest && prior.callID != call.ID {
		if _, visible := r.live[prior.callID]; visible {
			result.Content = msg.Blocks{msg.Text(fmt.Sprintf(
				"[Unchanged since call %s: this %s call returned the same %d bytes. The earlier result is still in the conversation above.]",
				prior.callID, call.Name, text.Len()-len(result.Content)))}
			return
		}
	}
	if _, ok := r.entries[key]; !ok {
		if len(r.order) >= resultReuseEntries {
			delete(r.entries, r.order[0])
			r.order = r.order[1:]
		}
		r.order = append(r.order, key)
	}
	r.entries[key] = reuseEntry{callID: call.ID, digest: digest}
}
