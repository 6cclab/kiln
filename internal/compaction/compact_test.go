package compaction

import (
	"context"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// fakeStreamer is a Streamer that returns one scripted assistant message,
// recording every call's transcript and options for assertions.
type fakeStreamer struct {
	response *msg.AssistantMessage
	err      error
	calls    []fakeCall
}

type fakeCall struct {
	model      provider.Model
	transcript []msg.Message
	opts       provider.StreamOptions
}

func (f *fakeStreamer) Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	f.calls = append(f.calls, fakeCall{model: model, transcript: transcript, opts: opts})
	ch := make(chan msg.StreamEvent)
	close(ch)
	return ch, func() (*msg.AssistantMessage, error) { return f.response, f.err }
}

func scriptedAssistant(text string, usage msg.Usage) *msg.AssistantMessage {
	return &msg.AssistantMessage{
		Role:       msg.RoleAssistant,
		StopReason: msg.StopStop,
		Content:    msg.Blocks{msg.Text(text)},
		Usage:      usage,
	}
}

func testModel() provider.Model {
	return provider.Model{ID: "test-model", Api: provider.ApiAnthropicMessages, ContextWindow: 100000, MaxTokens: 8192}
}

// TestCompactSimplePrompt asserts Compact, given a non-split-turn
// Preparation, sends a prompt containing SerializeConversation's output and
// the custom instructions, and returns a Result whose shape matches what a
// caller stores on a session.EntryCompaction (Summary, TokensBefore, Usage,
// RetainedTail, Details).
func TestCompactSimplePrompt(t *testing.T) {
	prep := &Preparation{
		MessagesToSummarize: []msg.Message{
			msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("please refactor foo.go")}},
			msg.AssistantMessage{
				Role: msg.RoleAssistant, StopReason: msg.StopToolUse,
				Content: msg.Blocks{msg.NewToolCall("call-1", "edit", map[string]any{"path": "foo.go"})},
			},
		},
		RetainedTail: []msg.Message{
			msg.AssistantMessage{Role: msg.RoleAssistant, StopReason: msg.StopStop, Content: msg.Blocks{msg.Text("done")}},
		},
		IsSplitTurn:  false,
		TokensBefore: 12345,
		FileOps: FileOperations{
			Read:    map[string]struct{}{},
			Written: map[string]struct{}{},
			Edited:  map[string]struct{}{"foo.go": {}},
		},
		Settings: Settings{Enabled: true, ReserveTokens: 4000, KeepRecentTokens: 2000},
	}

	wantSummaryText := "## Goal\nRefactor foo.go\n"
	usage := msg.Usage{Input: 100, Output: 50, TotalTokens: 150}
	streamer := &fakeStreamer{response: scriptedAssistant(wantSummaryText, usage)}

	custom := "focus on error handling"
	result, err := Compact(context.Background(), prep, streamer, testModel(), &custom, provider.ThinkingOff)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	if len(streamer.calls) != 1 {
		t.Fatalf("streamer called %d times, want 1", len(streamer.calls))
	}
	call := streamer.calls[0]
	if call.opts.SystemPrompt != SummarizationSystemPrompt {
		t.Fatalf("SystemPrompt = %q, want pi's SUMMARIZATION_SYSTEM_PROMPT", call.opts.SystemPrompt)
	}
	sentText := msg.TextOf(call.transcript[0].(msg.UserMessage).Content)

	wantConversation := SerializeConversation(prep.MessagesToSummarize)
	if !strings.Contains(sentText, wantConversation) {
		t.Fatalf("prompt does not contain SerializeConversation output.\nprompt: %s\nwant substring: %s", sentText, wantConversation)
	}
	if !strings.Contains(sentText, "Additional focus: "+custom) {
		t.Fatalf("prompt does not contain the custom instructions.\nprompt: %s", sentText)
	}

	if !strings.HasPrefix(result.Summary, wantSummaryText) {
		t.Fatalf("Summary = %q, want it to start with the scripted summary text", result.Summary)
	}
	if !strings.Contains(result.Summary, "<modified-files>\nfoo.go\n</modified-files>") {
		t.Fatalf("Summary = %q, want it to contain the modified-files tag for foo.go", result.Summary)
	}
	if result.TokensBefore != prep.TokensBefore {
		t.Fatalf("TokensBefore = %d, want %d", result.TokensBefore, prep.TokensBefore)
	}
	if result.Usage != usage {
		t.Fatalf("Usage = %+v, want %+v", result.Usage, usage)
	}
	if len(result.RetainedTail) != 1 {
		t.Fatalf("RetainedTail = %d messages, want 1", len(result.RetainedTail))
	}
	if len(result.Details.ModifiedFiles) != 1 || result.Details.ModifiedFiles[0] != "foo.go" {
		t.Fatalf("Details.ModifiedFiles = %v, want [foo.go]", result.Details.ModifiedFiles)
	}
}

// TestCompactSplitTurn asserts a split-turn Preparation triggers two model
// calls (history summary, then turn-prefix summary) and joins their text
// with pi's exact "Turn Context (split turn)" separator.
func TestCompactSplitTurn(t *testing.T) {
	prep := &Preparation{
		MessagesToSummarize: []msg.Message{
			msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("earlier turn")}},
		},
		TurnPrefixMessages: []msg.Message{
			msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("start of the big turn")}},
		},
		RetainedTail: []msg.Message{
			msg.AssistantMessage{Role: msg.RoleAssistant, StopReason: msg.StopStop, Content: msg.Blocks{msg.Text("end of the big turn")}},
		},
		IsSplitTurn:  true,
		TokensBefore: 500,
		FileOps:      CreateFileOps(),
		Settings:     Settings{Enabled: true, ReserveTokens: 4000, KeepRecentTokens: 2000},
	}

	responses := []*msg.AssistantMessage{
		scriptedAssistant("HISTORY SUMMARY", msg.Usage{TotalTokens: 10}),
		scriptedAssistant("PREFIX SUMMARY", msg.Usage{TotalTokens: 20}),
	}
	streamer := &scriptedStreamer{responses: responses}

	result, err := Compact(context.Background(), prep, streamer, testModel(), nil, provider.ThinkingOff)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(streamer.calls) != 2 {
		t.Fatalf("streamer called %d times, want 2", len(streamer.calls))
	}
	wantJoin := "HISTORY SUMMARY\n\n---\n\n**Turn Context (split turn):**\n\nPREFIX SUMMARY"
	if !strings.HasPrefix(result.Summary, wantJoin) {
		t.Fatalf("Summary = %q, want it to start with %q", result.Summary, wantJoin)
	}
	if result.Usage.TotalTokens != 30 {
		t.Fatalf("Usage.TotalTokens = %d, want 30 (history 10 + prefix 20)", result.Usage.TotalTokens)
	}
	// Second call's prompt must be the turn-prefix conversation plus the
	// turn-prefix prompt, not the update/summarization one.
	secondText := msg.TextOf(streamer.calls[1].transcript[0].(msg.UserMessage).Content)
	if !strings.Contains(secondText, "This is the PREFIX of a turn that was too large to keep.") {
		t.Fatalf("second call prompt = %q, want the turn-prefix prompt", secondText)
	}
}

// TestCompactAbortedSurfacesError asserts a StopAborted response becomes an
// *Error with Code "aborted", matching pi's CompactionError("aborted", ...).
func TestCompactAbortedSurfacesError(t *testing.T) {
	prep := &Preparation{
		Settings: Settings{Enabled: true, ReserveTokens: 4000, KeepRecentTokens: 2000},
		FileOps:  CreateFileOps(),
	}
	streamer := &fakeStreamer{response: &msg.AssistantMessage{Role: msg.RoleAssistant, StopReason: msg.StopAborted, ErrorMessage: "user cancelled"}}

	_, err := Compact(context.Background(), prep, streamer, testModel(), nil, provider.ThinkingOff)
	if err == nil {
		t.Fatal("Compact returned nil error, want an aborted Error")
	}
	var ce *Error
	if !isCompactionError(err, &ce) {
		t.Fatalf("error is %T, want *compaction.Error", err)
	}
	if ce.Code != "aborted" {
		t.Fatalf("Code = %q, want %q", ce.Code, "aborted")
	}
}

// scriptedStreamer returns one response per call, in order.
type scriptedStreamer struct {
	responses []*msg.AssistantMessage
	calls     []fakeCall
}

func (s *scriptedStreamer) Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	i := len(s.calls)
	s.calls = append(s.calls, fakeCall{model: model, transcript: transcript, opts: opts})
	ch := make(chan msg.StreamEvent)
	close(ch)
	return ch, func() (*msg.AssistantMessage, error) {
		if i >= len(s.responses) {
			return nil, nil
		}
		return s.responses[i], nil
	}
}

func isCompactionError(err error, out **Error) bool {
	ce, ok := err.(*Error)
	if ok {
		*out = ce
	}
	return ok
}
