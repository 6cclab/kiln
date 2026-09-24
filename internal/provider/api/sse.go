// Package api implements one HTTP streaming client per API shape (this
// phase: anthropic-messages and openai-completions), ported from pi-ai's
// dist/api/anthropic-messages.js and openai-completions.js.
//
// Every client emits msg.StreamEvent with the live-mutating Partial
// semantics: the same *msg.AssistantMessage is mutated in place across
// events and handed back as the Message on "done" / Error on "error".
package api

import (
	"bufio"
	"strings"
)

// sseEvent is one decoded Server-Sent Event: an optional event name and its
// joined data lines.
type sseEvent struct {
	Event string
	Data  string
}

// scanSSE reads Server-Sent Events from r, calling emit for each one. It
// stops on read error or EOF. Comment lines (leading ':') are ignored. A
// bare "data: [DONE]" (OpenAI's stream terminator) is delivered to emit like
// any other event; callers recognize it by Data.
func scanSSE(r *bufio.Reader, emit func(sseEvent)) error {
	var event string
	var data []string
	flush := func() {
		if event == "" && len(data) == 0 {
			return
		}
		emit(sseEvent{Event: event, Data: strings.Join(data, "\n")})
		event = ""
		data = nil
	}
	for {
		line, err := r.ReadString('\n')
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			flush()
		} else if strings.HasPrefix(trimmed, ":") {
			// comment / keep-alive, ignore
		} else if v, ok := cutPrefix(trimmed, "event:"); ok {
			event = strings.TrimSpace(v)
		} else if v, ok := cutPrefix(trimmed, "data:"); ok {
			data = append(data, strings.TrimPrefix(v, " "))
		}
		if err != nil {
			flush()
			return err
		}
	}
}

func cutPrefix(s, prefix string) (string, bool) {
	if strings.HasPrefix(s, prefix) {
		return s[len(prefix):], true
	}
	return "", false
}
