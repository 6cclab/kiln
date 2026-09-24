package compaction

import (
	"fmt"
	"sort"
	"strings"

	"github.com/andrepato/harness/internal/msg"
)

// toolResultMaxChars is pi's TOOL_RESULT_MAX_CHARS (harness/compaction/
// utils.js): a tool result's text is truncated to this many characters in
// a summarization prompt.
const toolResultMaxChars = 2000

func truncateForSummary(text string, maxChars int) string {
	if len(text) <= maxChars {
		return text
	}
	truncatedChars := len(text) - maxChars
	return fmt.Sprintf("%s\n\n[... %d more characters truncated]", text[:maxChars], truncatedChars)
}

// sortedArgKeys returns tc.Arguments' keys in alphabetical order. pi's
// tool-call rendering preserves the model's own emission order (a parsed
// JS object's insertion order); Go's map[string]any has no such order, so
// this port sorts for determinism instead. See doc.go.
func sortedArgKeys(args map[string]any) []string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// SerializeConversation is pi's serializeConversation (harness/compaction/
// utils.js) applied to messages that have already been through this port's
// eager convertToLlm-equivalent step (see doc.go), i.e. every message here
// is a system/user/assistant/toolResult message. System messages are
// skipped, matching pi's switch (which has no "system" case).
func SerializeConversation(messages []msg.Message) string {
	var parts []string
	for _, m := range messages {
		switch t := m.(type) {
		case msg.UserMessage:
			if content := msg.TextOf(t.Content); content != "" {
				parts = append(parts, "[User]: "+content)
			}
		case msg.AssistantMessage:
			var thinkingParts []string
			var toolCalls []string
			hasText := false
			for _, b := range t.Content {
				switch bl := b.(type) {
				case msg.ThinkingContent:
					thinkingParts = append(thinkingParts, bl.Thinking)
				case msg.ToolCall:
					var argParts []string
					for _, k := range sortedArgKeys(bl.Arguments) {
						argParts = append(argParts, k+"="+safeJSONStringify(bl.Arguments[k]))
					}
					toolCalls = append(toolCalls, bl.Name+"("+strings.Join(argParts, ", ")+")")
				case msg.TextContent:
					hasText = true
				}
			}
			if len(thinkingParts) > 0 {
				parts = append(parts, "[Assistant thinking]: "+strings.Join(thinkingParts, "\n"))
			}
			if hasText {
				parts = append(parts, "[Assistant]: "+msg.TextOf(t.Content))
			}
			if len(toolCalls) > 0 {
				parts = append(parts, "[Assistant tool calls]: "+strings.Join(toolCalls, "; "))
			}
		case msg.ToolResultMessage:
			if content := msg.TextOf(t.Content); content != "" {
				parts = append(parts, "[Tool result]: "+truncateForSummary(content, toolResultMaxChars))
			}
		}
	}
	return strings.Join(parts, "\n\n")
}
