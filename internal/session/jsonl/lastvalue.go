package jsonl

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// LastValue returns the value last set at (namespace, key) in the session
// file at path, without opening the session: it reads the file line by
// line and parses only the lines that mention the namespace. ok is false
// when the file cannot be read or the value was never set (or was
// deleted). The caller learns, before choosing a model, which model a
// session it is about to resume last ran on.
func LastValue(path, namespace, key string) (json.RawMessage, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	needle := []byte(`"` + namespace + `"`)
	var last json.RawMessage
	r := bufio.NewReaderSize(f, 256<<10)
	for first := true; ; first = false {
		line, err := r.ReadBytes('\n')
		if !first && bytes.Contains(line, needle) {
			if writes, perr := ParseTransaction(line); perr == nil {
				for _, w := range writes {
					if w.Value == nil || w.Value.Kind != "value" || w.Value.Namespace != namespace || w.Value.Key != key {
						continue
					}
					if w.Value.Op == "delete" {
						last = nil
					} else {
						last = w.Value.Value
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	return last, last != nil
}

// LastResponseModel returns the provider and model of the newest
// successful assistant reply recorded in the session file at path (a
// failed or aborted request is skipped, as pi's findLatestResponse does),
// without opening the session. Replies always name the physical model
// that answered. ok is false when the file cannot be read or holds none.
func LastResponseModel(path string) (providerID, modelID string, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", false
	}
	defer f.Close()
	needle := []byte(`"assistant"`)
	r := bufio.NewReaderSize(f, 256<<10)
	for first := true; ; first = false {
		line, err := r.ReadBytes('\n')
		if !first && bytes.Contains(line, needle) {
			if writes, perr := ParseTransaction(line); perr == nil {
				for _, w := range writes {
					if w.Entry == nil || w.Entry.Type != session.EntryMessage {
						continue
					}
					am, isAssistant := w.Entry.Message.(msg.AssistantMessage)
					if !isAssistant || am.StopReason == msg.StopError || am.StopReason == msg.StopAborted || am.Provider == "" || am.Model == "" {
						continue
					}
					providerID, modelID, ok = am.Provider, am.Model, true
				}
			}
		}
		if err != nil {
			break
		}
	}
	return providerID, modelID, ok
}
