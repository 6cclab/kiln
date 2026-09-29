package jsonl

import (
	"bufio"
	"io"
	"os"
	"strings"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// firstPromptScanLimit bounds how much of a session file FirstPrompt
// reads: the first prompt is near the top, and a listing of many sessions
// must not read each one whole.
const firstPromptScanLimit = 512 << 10

// FirstPrompt returns the text of the first user message recorded in the
// session file at path, on one line, or "" when none is found within the
// scan limit. /resume shows it so a session can be recognised by what it
// was about rather than by its id.
func FirstPrompt(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	r := bufio.NewReaderSize(io.LimitReader(f, firstPromptScanLimit), 64<<10)
	for first := true; ; first = false {
		line, err := r.ReadBytes('\n')
		if !first && len(line) > 0 {
			if writes, perr := ParseTransaction(line); perr == nil {
				for _, w := range writes {
					if text := userText(w); text != "" {
						return text
					}
				}
			}
		}
		if err != nil {
			return ""
		}
	}
}

func userText(w session.CommittedWrite) string {
	if w.Entry == nil || w.Entry.Type != session.EntryMessage {
		return ""
	}
	um, ok := w.Entry.Message.(msg.UserMessage)
	if !ok {
		return ""
	}
	return strings.Join(strings.Fields(msg.TextOf(um.Content)), " ")
}
