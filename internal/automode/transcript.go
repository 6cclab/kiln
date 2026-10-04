package automode

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// The transcript boundary. This is the classifier's defence against prompt
// injection, so it is spelled out here and nowhere else.
//
// The classifier sees:
//   - what the user typed, each message as typed: msg.UserMessage.KilnTyped,
//     which kiln stores with every prompt and follow-up a user typed, apart
//     from the hook context, @file contents or command expansion it built
//     around the line;
//   - in a subagent, the root session's typed lines as the user's intent,
//     and the subagent's own task as {"delegated_task": …}: the parent model
//     wrote it, so it is data, never the user's authorization;
//   - the tool calls the agent made before this one (name and input),
//     except read-only lookups (settings.ReadOnly), which change nothing;
//   - the CLAUDE.md memory the session loaded (Classifier.Memory);
//   - the action under review.
//
// It never sees:
//   - tool results: file contents, command output, fetched pages, MCP
//     responses. They are where hostile text arrives, and none of it may
//     steer the classifier;
//   - the agent's own prose and reasoning (text and thinking blocks). The
//     agent can be steered by what it read, so its words are not evidence
//     of what the user wants;
//   - compaction and branch summaries, which a model wrote from tool
//     results;
//   - a user message with no typed line recorded (a session written before
//     kiln recorded them, kiln's own follow-ups): it is replaced by a note,
//     not shown, because kiln cannot tell what in it the user typed.
//
// Every value is JSON-encoded (Go escapes <, > and &, so no value can close
// a tag) and invisible format characters — Unicode tags, bidi controls,
// zero-width characters — are written as \u escapes, so text cannot hide
// from a reader or reorder what it sees.
//
// Claude Code draws the same line (code.claude.com/docs/en/permission-modes,
// "How the classifier evaluates actions": user messages, tool calls other
// than read-only lookups and CLAUDE.md; tool results stripped). Unlike
// Claude Code, the transcript is read from the whole branch, so a boundary
// the user stated before a compaction still counts.

// unrecordedNote replaces a user message with no typed line recorded.
const unrecordedNote = "a message kiln did not record as typed by the user was left out"

// EntryLister is the part of a harness lane the transcript is read from.
type EntryLister interface {
	FindEntries(ctx context.Context) ([]session.Entry, error)
}

// BranchMessages returns the messages on lane's branch, oldest first: only
// real message entries, never a compaction or branch summary. An error
// gives no history, which leaves the classifier with less reason to allow.
func BranchMessages(ctx context.Context, lane EntryLister) []msg.Message {
	entries, err := lane.FindEntries(ctx) // newest first
	if err != nil {
		return nil
	}
	out := make([]msg.Message, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		if e := entries[i]; e.Type == session.EntryMessage && e.Message != nil {
			out = append(out, e.Message)
		}
	}
	return out
}

// Limits on what one request carries, so a long session cannot outgrow a
// small classifier model. The oldest entries go first.
const (
	maxUserChars     = 4000
	maxToolInput     = 2000
	maxTranscript    = 60000
	maxMemoryChars   = 20000
	truncationSuffix = " …[truncated]"
)

// transcriptLines turns history into the classifier's transcript, one JSON
// object per line. userHistory, set for a subagent, is the root session's
// history: only its user lines are taken. In history, a user message is the
// user's when delegated is false, and the parent agent's task when true.
func transcriptLines(userHistory, history []msg.Message, delegated bool, skipCallID string) []string {
	var lines []string
	add := func(v any) {
		if b, err := json.Marshal(v); err == nil {
			lines = append(lines, escapeInvisible(string(b)))
		}
	}
	for _, m := range userHistory {
		if u, ok := asUser(m); ok {
			if line, ok := userLine(u); ok {
				add(line)
			}
		}
	}
	for _, m := range history {
		if u, ok := asUser(m); ok {
			if delegated {
				if text := msg.TextOf(u.Content); strings.TrimSpace(text) != "" {
					add(map[string]string{"delegated_task": clip(text, maxUserChars)})
				}
			} else if line, ok := userLine(u); ok {
				add(line)
			}
			continue
		}
		if a, ok := asAssistant(m); ok {
			for _, c := range toolCalls(a.Content, skipCallID) {
				add(c)
			}
		}
		// Tool results and system messages: never.
	}
	// Oldest first out, until the rest fits.
	total := 0
	for i := len(lines) - 1; i >= 0; i-- {
		total += len(lines[i]) + 1
		if total > maxTranscript {
			return append([]string{`{"note":"earlier conversation left out for length"}`}, lines[i+1:]...)
		}
	}
	return lines
}

// typedLines are the lines the user typed, whole (unclipped): the root
// session's (userHistory) and, outside a subagent, history's own. A
// subagent's user messages are its delegated task, never counted.
func typedLines(userHistory, history []msg.Message, delegated bool) []string {
	var out []string
	add := func(ms []msg.Message) {
		for _, m := range ms {
			if u, ok := asUser(m); ok && strings.TrimSpace(u.KilnTyped) != "" {
				out = append(out, u.KilnTyped)
			}
		}
	}
	add(userHistory)
	if !delegated {
		add(history)
	}
	return out
}

func asUser(m msg.Message) (msg.UserMessage, bool) {
	switch u := m.(type) {
	case msg.UserMessage:
		return u, true
	case *msg.UserMessage:
		if u != nil {
			return *u, true
		}
	}
	return msg.UserMessage{}, false
}

func asAssistant(m msg.Message) (msg.AssistantMessage, bool) {
	switch a := m.(type) {
	case msg.AssistantMessage:
		return a, true
	case *msg.AssistantMessage:
		if a != nil {
			return *a, true
		}
	}
	return msg.AssistantMessage{}, false
}

// userLine is a stored user message as the classifier sees it: the typed
// line, or a note when none was recorded. An empty message gives nothing.
func userLine(u msg.UserMessage) (map[string]string, bool) {
	if typed := strings.TrimSpace(u.KilnTyped); typed != "" {
		return map[string]string{"user": clip(u.KilnTyped, maxUserChars)}, true
	}
	if strings.TrimSpace(msg.TextOf(u.Content)) == "" {
		return nil, false
	}
	return map[string]string{"note": unrecordedNote}, true
}

type toolCallLine struct {
	Tool  string `json:"tool"`
	Input any    `json:"input"`
}

// toolCalls are an assistant message's tool calls, minus read-only lookups
// and the call under review.
func toolCalls(content msg.Blocks, skipCallID string) []toolCallLine {
	var out []toolCallLine
	for _, c := range msg.ToolCallsOf(content) {
		if c.ID == skipCallID && skipCallID != "" {
			continue
		}
		if settings.ReadOnly[strings.ToLower(c.Name)] {
			continue
		}
		out = append(out, toolCallLine{Tool: c.Name, Input: clipInput(c.Arguments, c.InvalidArgs)})
	}
	return out
}

// clipInput keeps a tool input whole when it is small, and otherwise as a
// clipped JSON string.
func clipInput(args map[string]any, invalid string) any {
	if invalid != "" {
		return clip(invalid, maxToolInput)
	}
	b, err := json.Marshal(args)
	if err != nil {
		return nil
	}
	if len(b) <= maxToolInput {
		return args
	}
	return clip(string(b), maxToolInput)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Cut on a rune boundary.
	cut := n
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut] + truncationSuffix
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// invisible reports a format character that renders as nothing or
// reorders text: Unicode tag characters, bidi embeddings, overrides,
// isolates and marks, zero-width characters and the byte-order mark.
func invisible(r rune) bool {
	switch {
	case r >= 0xE0000 && r <= 0xE007F: // tags
		return true
	case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069: // bidi embeddings, overrides, isolates
		return true
	case r == 0x200E, r == 0x200F, r == 0x061C: // bidi marks
		return true
	case r >= 0x200B && r <= 0x200D, r == 0x2060, r == 0xFEFF: // zero-width
		return true
	}
	return false
}

// escapeInvisible writes every invisible character in an encoded JSON text
// as a \u escape (a surrogate pair beyond the BMP). Such characters can
// only occur inside JSON strings, where an escape means the same thing, so
// the value is unchanged and the character can no longer hide.
func escapeInvisible(s string) string {
	if strings.IndexFunc(s, invisible) < 0 {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if !invisible(r) {
			b.WriteRune(r)
			continue
		}
		if r > 0xFFFF {
			r -= 0x10000
			fmt.Fprintf(&b, `\u%04x\u%04x`, 0xD800+(r>>10), 0xDC00+(r&0x3FF))
			continue
		}
		fmt.Fprintf(&b, `\u%04x`, r)
	}
	return b.String()
}
