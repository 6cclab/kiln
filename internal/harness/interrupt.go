package harness

import (
	"encoding/json"
	"strings"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/session"
)

// commitInterrupted keeps what a reply had streamed when the user
// interrupted it, as Claude Code does: the text stays in the transcript
// (EventMessageEnd, so the TUI commits it like any reply) and in the
// conversation, marked aborted, and what the request cost is counted. It
// returns the branch tip after the commit.
//
// Only text is kept. A tool call cut off mid-stream has no result and
// could not be sent back to a provider; reasoning is the model's scratch
// work. The provider reports output tokens only at the end of a stream, so
// an interrupted request's output is estimated from what streamed (chars/4)
// when the provider's own count is lower, and its cost recomputed from the
// model's rates: an estimate, but a closer one than nothing.
func (l *Lane) commitInterrupted(operationID, tip, responseEntryID string, partial *msg.AssistantMessage, cfg session.LaneConfiguration) string {
	if partial == nil || responseEntryID == "" {
		return tip
	}
	usage := partial.Usage
	streamed := 0
	var kept msg.Blocks
	for _, b := range partial.Content {
		switch c := b.(type) {
		case msg.TextContent:
			streamed += len(c.Text)
			if strings.TrimSpace(c.Text) != "" {
				kept = append(kept, msg.Text(c.Text))
			}
		case msg.ThinkingContent:
			streamed += len(c.Thinking)
		case msg.ToolCall:
			streamed += len(c.InvalidArgs)
			if b, err := json.Marshal(c.Arguments); err == nil && len(c.Arguments) > 0 {
				streamed += len(b)
			}
		}
	}
	if est := (streamed + 3) / 4; est > usage.Output {
		usage.Output = est
	}
	if usage.TotalTokens != 0 {
		usage.TotalTokens = usage.Input + usage.Output + usage.CacheRead + usage.CacheWrite
	}
	if m, ok := l.h.opts.Registry.GetModel(cfg.Model.Provider, cfg.Model.ModelID); ok {
		provider.ApplyCost(m, &usage)
	}
	if usage.Input == 0 && usage.Output == 0 && usage.CacheRead == 0 && usage.CacheWrite == 0 {
		return tip
	}

	writes := []session.Write{session.DeleteListWrite(session.PendingAssistantFrames(operationID, responseEntryID))}
	row := session.UsageRow{ID: l.newID(), Usage: usage}
	var reply *msg.AssistantMessage
	if len(kept) > 0 {
		am := *partial
		am.Content = kept
		am.Usage = usage
		am.StopReason = msg.StopAborted
		am.ErrorMessage = ""
		reply = &am
		entry := session.Entry{ID: responseEntryID, Type: session.EntryMessage, Message: am}
		if tip != "" {
			p := tip
			entry.ParentID = &p
		}
		tipW, err := session.SetValue(session.BranchTip(l.name), &responseEntryID)
		if err != nil {
			return tip
		}
		row.EntryID = responseEntryID
		writes = append(writes, session.EntryWrite{Entry: entry}, tipW)
	}
	writes = append(writes, session.UsageWrite{Row: row})
	if _, err := l.h.opts.Storage.Commit(writes); err != nil {
		return tip
	}
	if reply != nil {
		l.h.events.Emit(Event{Type: EventMessageEnd, Lane: l.name, OperationID: operationID, EntryID: responseEntryID, Message: reply})
		l.h.events.Emit(Event{Type: EventEntryAdded, Lane: l.name, EntryID: responseEntryID, ParentID: tip})
		tip = responseEntryID
	}
	totals := l.h.opts.Storage.GetStats().Usage
	l.h.events.Emit(Event{Type: EventUsage, Lane: l.name, OperationID: operationID, UsageRow: &usage, UsageTotals: &totals})
	return tip
}
