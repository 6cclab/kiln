package automode

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// The transcript boundary. This is the classifier's defence against prompt
// injection, so it is spelled out here and nowhere else.
//
// The classifier sees:
//   - what the user typed, each message as typed;
//   - the tool calls the agent made before this one (name and input),
//     except read-only lookups (settings.ReadOnly), which change nothing;
//   - the user's CLAUDE.md memory, labelled as user configuration
//     (Classifier.Memory);
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
//   - what kiln attaches to a user message: the <hook-context> block from
//     hooks and the <file> blocks an @mention inlines. Intents maps each
//     stored prompt back to the line the user typed. A stored message kiln
//     cannot map (one from before this process started, on a resumed
//     session) that starts with either attachment is left out whole rather
//     than parsed: a file's contents could imitate the boundary.
//
// Claude Code draws the same line (code.claude.com/docs/en/permission-modes,
// "How the classifier evaluates actions": user messages, tool calls other
// than read-only lookups and CLAUDE.md; tool results stripped). Unlike
// Claude Code, the transcript is read from the whole branch, so a boundary
// the user stated before a compaction still counts.

// Intents remembers, for each prompt kiln sent, the line the user typed:
// the stored message carries hook context and @file contents the classifier
// must not see. Safe for concurrent use. The zero value is ready.
type Intents struct {
	mu    sync.Mutex
	typed map[string]string
}

// Record notes that the user message stored as stored was typed as typed.
// Nothing is recorded when they are equal: an unmapped message is read as
// typed unless it starts with an attachment.
func (in *Intents) Record(stored, typed string) {
	if in == nil || stored == typed {
		return
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.typed == nil {
		in.typed = map[string]string{}
	}
	in.typed[stored] = typed
}

func (in *Intents) lookup(stored string) (string, bool) {
	if in == nil {
		return "", false
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	t, ok := in.typed[stored]
	return t, ok
}

// attachmentPrefixes start a stored user message kiln built around the
// typed line (internal/cli's prompt assembly and ResolveMentions).
var attachmentPrefixes = []string{"<hook-context>", `<file path="`}

// omittedUserMessage replaces a user message whose typed text kiln cannot
// separate from what it attached.
const omittedUserMessage = "[a message with attached files or hook output; left out]"

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

// transcriptLines turns history into the classifier's transcript: one JSON
// object per line, {"user": text} or {"tool": name, "input": …}. JSON
// encoding keeps every value data: Go escapes <, > and & in strings, so no
// value can close the tags the request wraps the transcript in.
func transcriptLines(history []msg.Message, intents *Intents, skipCallID string) []string {
	var lines []string
	add := func(v any) {
		if b, err := json.Marshal(v); err == nil {
			lines = append(lines, string(b))
		}
	}
	for _, m := range history {
		switch m := m.(type) {
		case msg.UserMessage:
			if text, ok := userText(m.Content, intents); ok {
				add(map[string]string{"user": clip(text, maxUserChars)})
			}
		case *msg.UserMessage:
			if text, ok := userText(m.Content, intents); ok {
				add(map[string]string{"user": clip(text, maxUserChars)})
			}
		case msg.AssistantMessage:
			for _, c := range toolCalls(m.Content, skipCallID) {
				add(c)
			}
		case *msg.AssistantMessage:
			for _, c := range toolCalls(m.Content, skipCallID) {
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

// userText is what the user typed in a stored user message: text blocks
// only (an image is not intent), mapped back through intents.
func userText(content msg.Blocks, intents *Intents) (string, bool) {
	text := msg.TextOf(content)
	if strings.TrimSpace(text) == "" {
		return "", false
	}
	if typed, ok := intents.lookup(text); ok {
		return typed, true
	}
	for _, p := range attachmentPrefixes {
		if strings.HasPrefix(text, p) {
			return omittedUserMessage, true
		}
	}
	return text, true
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
