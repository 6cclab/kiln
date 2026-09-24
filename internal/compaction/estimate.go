package compaction

import (
	"encoding/json"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// estimatedImageChars is pi's ESTIMATED_IMAGE_CHARS
// (harness/compaction/compaction.js): a flat per-image character estimate
// used because pi does not decode image bytes to count tokens.
const estimatedImageChars = 4800

func ceilDiv4(chars int) int {
	return (chars + 3) / 4
}

func safeJSONStringify(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[unserializable]"
	}
	return string(b)
}

// EstimateBlocks estimates the token count of a text/image content list
// using pi's conservative character heuristic (harness/compaction/
// compaction.js:estimateTextAndImageContentChars, chars/4 rounded up).
// Blocks other than text and image (thinking, toolCall) are ignored here,
// matching pi: those only ever appear inside assistant content, which
// EstimateTokens handles separately.
func EstimateBlocks(blocks msg.Blocks) int {
	chars := 0
	for _, b := range blocks {
		switch t := b.(type) {
		case msg.TextContent:
			chars += len(t.Text)
		case msg.ImageContent:
			chars += estimatedImageChars
		}
	}
	return ceilDiv4(chars)
}

func estimateTextAndImageChars(blocks msg.Blocks) int {
	chars := 0
	for _, b := range blocks {
		switch t := b.(type) {
		case msg.TextContent:
			chars += len(t.Text)
		case msg.ImageContent:
			chars += estimatedImageChars
		}
	}
	return chars
}

// EstimateTokens estimates one message's token count, porting pi's
// estimateTokens (harness/compaction/compaction.js) role by role. A
// SystemMessage returns 0, matching pi (its switch has no "system" case).
func EstimateTokens(m msg.Message) int {
	switch v := m.(type) {
	case msg.UserMessage:
		return ceilDiv4(estimateTextAndImageChars(v.Content))
	case msg.AssistantMessage:
		chars := 0
		for _, b := range v.Content {
			switch bl := b.(type) {
			case msg.TextContent:
				chars += len(bl.Text)
			case msg.ThinkingContent:
				chars += len(bl.Thinking)
			case msg.ToolCall:
				chars += len(bl.Name) + len(safeJSONStringify(bl.Arguments))
			}
		}
		return ceilDiv4(chars)
	case msg.ToolResultMessage:
		return ceilDiv4(estimateTextAndImageChars(v.Content))
	default: // msg.SystemMessage and anything else
		return 0
	}
}

// TokensFromUsage is pi's calculateContextTokens(usage)
// (harness/compaction/compaction.js): totalTokens when reported, else the
// sum of the four counted fields.
func TokensFromUsage(u msg.Usage) int {
	if u.TotalTokens != 0 {
		return u.TotalTokens
	}
	return u.Input + u.Output + u.CacheRead + u.CacheWrite
}

// ContextUsageEstimate is pi's ContextUsageEstimate
// (harness/compaction/compaction.d.ts).
type ContextUsageEstimate struct {
	// Tokens is the estimated total context tokens.
	Tokens int
	// UsageTokens is tokens reported by the most recent assistant usage
	// block found, or 0 if none was found.
	UsageTokens int
	// TrailingTokens is the estimated tokens after that usage block (or, if
	// none was found, the same as Tokens).
	TrailingTokens int
	// LastUsageIndex is the index into the context-message list that
	// supplied UsageTokens, or nil when none was found. Exposed for tests
	// and diagnostics; callers of CalculateContextTokens normally only need
	// Tokens.
	LastUsageIndex *int
}

// isContextMessage mirrors session/context.js's isContextMessage: every
// message counts except an assistant message that ended in error, was
// aborted, or was deferred (deferred responses have no committed content
// yet).
func isContextMessage(m msg.Message) bool {
	am, ok := m.(msg.AssistantMessage)
	if !ok {
		return true
	}
	switch am.StopReason {
	case msg.StopError, msg.StopAborted, msg.StopDeferred:
		return false
	default:
		return true
	}
}

// buildContextEntries mirrors session/context.js's buildContextEntries:
// find the last compaction entry (if any) and keep only it plus everything
// after it, discarding the (now-summarized) history before it. This is the
// "compaction entries reset the count" rule CalculateContextTokens follows.
func buildContextEntries(entries []session.Entry) []session.Entry {
	compactionIdx := -1
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Type == session.EntryCompaction {
			compactionIdx = i
			break
		}
	}
	if compactionIdx == -1 {
		return entries
	}
	out := make([]session.Entry, 0, len(entries)-compactionIdx)
	out = append(out, entries[compactionIdx])
	out = append(out, entries[compactionIdx+1:]...)
	return out
}

// sessionEntryToContextMessages mirrors session/context.js's
// sessionEntryToContextMessages.
func sessionEntryToContextMessages(e session.Entry) []msg.Message {
	switch e.Type {
	case session.EntryMessage:
		if e.Message != nil && isContextMessage(e.Message) {
			return []msg.Message{e.Message}
		}
		return nil
	case session.EntryCompaction:
		out := make([]msg.Message, 0, 1+len(e.RetainedTail))
		out = append(out, wrapCompactionSummary(e))
		for _, m := range e.RetainedTail {
			if isContextMessage(m) {
				out = append(out, m)
			}
		}
		return out
	case session.EntryBranchSummary:
		if e.Summary == "" {
			return nil
		}
		return []msg.Message{wrapBranchSummary(e)}
	default: // EntryCustom
		return nil
	}
}

// estimateContextTokens is pi's estimateContextTokens
// (harness/compaction/compaction.js): scan from the end for the most
// recent valid assistant usage, then add the estimated size of everything
// after it. If none is found, every message is estimated.
func estimateContextTokens(messages []msg.Message) ContextUsageEstimate {
	lastIdx := -1
	var lastUsage msg.Usage
	for i := len(messages) - 1; i >= 0; i-- {
		am, ok := messages[i].(msg.AssistantMessage)
		if !ok {
			continue
		}
		if am.StopReason == msg.StopAborted || am.StopReason == msg.StopError {
			continue
		}
		if TokensFromUsage(am.Usage) > 0 {
			lastIdx = i
			lastUsage = am.Usage
			break
		}
	}
	if lastIdx == -1 {
		total := 0
		for _, m := range messages {
			total += EstimateTokens(m)
		}
		return ContextUsageEstimate{Tokens: total, TrailingTokens: total}
	}
	usageTokens := TokensFromUsage(lastUsage)
	trailing := 0
	for i := lastIdx + 1; i < len(messages); i++ {
		trailing += EstimateTokens(messages[i])
	}
	idx := lastIdx
	return ContextUsageEstimate{
		Tokens:         usageTokens + trailing,
		UsageTokens:    usageTokens,
		TrailingTokens: trailing,
		LastUsageIndex: &idx,
	}
}

// CalculateContextTokens estimates the context tokens a session path
// currently represents, applying pi's rule that a compaction entry resets
// the count: only the last compaction entry (turned into its summary
// message plus its retained tail) and whatever comes after it are counted;
// everything the compaction folded away is not walked again. Ports pi's
// combination of buildContextEntries + sessionEntryToContextMessages +
// estimateContextTokens (session/context.js, harness/compaction/
// compaction.js).
func CalculateContextTokens(entries []session.Entry) ContextUsageEstimate {
	contextEntries := buildContextEntries(entries)
	var messages []msg.Message
	for _, e := range contextEntries {
		messages = append(messages, sessionEntryToContextMessages(e)...)
	}
	return estimateContextTokens(messages)
}

// ShouldCompact is pi's shouldCompact (harness/compaction/compaction.js).
func ShouldCompact(contextTokens, contextWindow int, settings Settings) bool {
	if !settings.Enabled {
		return false
	}
	return contextTokens > contextWindow-settings.ReserveTokens
}
