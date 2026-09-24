package compaction

import (
	"context"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// TestPrepareBranchSummaryDropsToolResults asserts PrepareBranchSummary
// (pi's prepareBranchEntries) excludes toolResult messages and custom
// entries, keeps user/assistant messages in chronological order, and
// extracts file operations from the assistant tool calls it keeps.
func TestPrepareBranchSummaryDropsToolResults(t *testing.T) {
	entries := []session.Entry{
		userEntry("u0", "look at bar.go"),
		{
			ID:   "a0",
			Type: session.EntryMessage,
			Message: msg.AssistantMessage{
				Role: msg.RoleAssistant, StopReason: msg.StopToolUse,
				Content: msg.Blocks{msg.NewToolCall("call-1", "read", map[string]any{"path": "bar.go"})},
			},
		},
		toolResultEntry("t0", "call-1", "package bar"),
		assistantTextEntry("a1", "bar.go looks fine"),
	}

	prep := PrepareBranchSummary(entries, 0)
	if len(prep.Messages) != 3 {
		t.Fatalf("Messages = %d, want 3 (toolResult dropped)", len(prep.Messages))
	}
	for _, m := range prep.Messages {
		if m.MessageRole() == msg.RoleToolResult {
			t.Fatalf("Messages contains a toolResult message: %+v", m)
		}
	}
	if _, ok := prep.FileOps.Read["bar.go"]; !ok {
		t.Fatalf("FileOps.Read = %+v, want it to contain bar.go", prep.FileOps.Read)
	}
}

// TestSummarizeBranchEmptyReturnsPlaceholder asserts SummarizeBranch on an
// empty BranchPreparation returns pi's exact placeholder without calling
// the model.
func TestSummarizeBranchEmptyReturnsPlaceholder(t *testing.T) {
	streamer := &fakeStreamer{}
	result, err := SummarizeBranch(context.Background(), BranchPreparation{}, streamer, testModel(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary != "No content to summarize" {
		t.Fatalf("Summary = %q, want %q", result.Summary, "No content to summarize")
	}
	if len(streamer.calls) != 0 {
		t.Fatalf("streamer was called %d times, want 0", len(streamer.calls))
	}
}

// TestSummarizeBranchWrapsWithPreamble asserts SummarizeBranch prefixes the
// model's text with pi's exact BRANCH_SUMMARY_PREAMBLE, appends file tags,
// and sends the default BRANCH_SUMMARY_PROMPT (not the compaction prompts)
// with maxTokens capped at 2048 and no reasoning applied even for a
// reasoning-capable model.
func TestSummarizeBranchWrapsWithPreamble(t *testing.T) {
	prep := BranchPreparation{
		Messages: []msg.Message{
			msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("explored an alternate approach")}},
		},
		FileOps: CreateFileOps(),
	}
	streamer := &fakeStreamer{response: scriptedAssistant("Tried approach X, it didn't pan out.", msg.Usage{TotalTokens: 5})}

	model := testModel()
	model.Reasoning = true
	result, err := SummarizeBranch(context.Background(), prep, streamer, model, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.Summary, branchSummaryPreamble) {
		t.Fatalf("Summary does not start with BRANCH_SUMMARY_PREAMBLE: %q", result.Summary)
	}
	if !strings.Contains(result.Summary, "Tried approach X") {
		t.Fatalf("Summary = %q, want the model's text", result.Summary)
	}

	if len(streamer.calls) != 1 {
		t.Fatalf("streamer called %d times, want 1", len(streamer.calls))
	}
	call := streamer.calls[0]
	if call.opts.MaxTokens != branchSummaryMaxTokens {
		t.Fatalf("MaxTokens = %d, want %d", call.opts.MaxTokens, branchSummaryMaxTokens)
	}
	if call.opts.ThinkingLevel != "" {
		t.Fatalf("ThinkingLevel = %q, want empty: pi's branch summary call never applies reasoning", call.opts.ThinkingLevel)
	}
	sentText := msg.TextOf(call.transcript[0].(msg.UserMessage).Content)
	if !strings.Contains(sentText, branchSummaryPrompt) {
		t.Fatal("prompt does not contain BRANCH_SUMMARY_PROMPT")
	}
}
