package compaction

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// liveTranscriptFixture is a small, realistic "what the agent loop would
// send next" transcript: a system prompt, one active tool, and a few
// real turns.
func liveTranscriptFixture() ([]msg.Message, string, []provider.ToolDef) {
	sysPrompt := "You are kiln, a coding agent. CLAUDE.md says: run make check before committing."
	tools := []provider.ToolDef{
		{Name: "bash", Description: "Run a shell command", Parameters: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`)},
		{Name: "edit", Description: "Edit a file", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)},
	}
	transcript := []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("please refactor foo.go")}, Timestamp: 1},
		msg.AssistantMessage{Role: msg.RoleAssistant, StopReason: msg.StopToolUse, Content: msg.Blocks{msg.NewToolCall("call-1", "edit", map[string]any{"path": "foo.go"})}, Timestamp: 2},
		msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: "call-1", Content: msg.Blocks{msg.Text("ok")}, Timestamp: 3},
		msg.AssistantMessage{Role: msg.RoleAssistant, StopReason: msg.StopStop, Content: msg.Blocks{msg.Text("done")}, Timestamp: 4},
	}
	return transcript, sysPrompt, tools
}

func fastModel() provider.Model {
	return provider.Model{ID: "test-model", Provider: "test", Api: provider.ApiAnthropicMessages, ContextWindow: 100000, MaxTokens: 8192}
}

// TestCompactFastPathSamePrefix is this task's required prefix-identity
// test: given a FastPathInput built from a live transcript, CompactWith
// sends exactly one request whose system prompt, tools and message
// prefix are byte/deep-identical to the live input -- plus exactly one
// appended user message -- with tool use instructed off, thinking off,
// and a bounded MaxTokens. This path and this assertion do not exist on
// origin/main: Options has no FastPath field there, so this test does not
// compile against it (see the task's "must fail on origin/main" ask,
// verified by running this file against a checkout of origin/main).
func TestCompactFastPathSamePrefix(t *testing.T) {
	transcript, sysPrompt, tools := liveTranscriptFixture()
	prep := &Preparation{
		MessagesToSummarize: transcript[:2],
		RetainedTail:        transcript[2:],
		TokensBefore:        999,
		FileOps:             CreateFileOps(),
		Settings:            Settings{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 2000},
	}
	wantSummary := "## Goal\nRefactor foo.go\n"
	usage := msg.Usage{Input: 100, Output: 50, TotalTokens: 150, CacheRead: 80}
	streamer := &fakeStreamer{response: scriptedAssistant(wantSummary, usage)}

	in := FastPathInput{SystemPrompt: sysPrompt, Tools: tools, Transcript: transcript}
	var partDone []PartDone
	result, err := CompactWith(context.Background(), prep, streamer, fastModel(), nil, provider.ThinkingOff, Options{
		FastPath:   &in,
		OnPartDone: func(pd PartDone) { partDone = append(partDone, pd) },
	})
	if err != nil {
		t.Fatalf("CompactWith: %v", err)
	}
	if len(streamer.calls) != 1 {
		t.Fatalf("streamer called %d times, want 1 (one request instead of a history request and a turn-prefix request)", len(streamer.calls))
	}
	call := streamer.calls[0]

	if call.opts.SystemPrompt != sysPrompt {
		t.Errorf("SystemPrompt = %q, want the live system prompt %q (not SummarizationSystemPrompt)", call.opts.SystemPrompt, sysPrompt)
	}
	if !reflect.DeepEqual(call.opts.Tools, tools) {
		t.Errorf("Tools = %+v, want the live tools %+v", call.opts.Tools, tools)
	}
	if call.opts.ThinkingLevel != provider.ThinkingOff {
		t.Errorf("ThinkingLevel = %q, want %q", call.opts.ThinkingLevel, provider.ThinkingOff)
	}
	if call.opts.MaxTokens <= 0 || call.opts.MaxTokens > summaryOutputCap {
		t.Errorf("MaxTokens = %d, want a positive bound <= %d", call.opts.MaxTokens, summaryOutputCap)
	}

	if len(call.transcript) != len(transcript)+1 {
		t.Fatalf("transcript has %d messages, want %d (the live prefix) + 1 (the appended summarization turn)", len(call.transcript), len(transcript)+1)
	}
	if !reflect.DeepEqual(call.transcript[:len(transcript)], transcript) {
		t.Errorf("transcript prefix != the live transcript verbatim:\ngot:  %+v\nwant: %+v", call.transcript[:len(transcript)], transcript)
	}
	appended, ok := call.transcript[len(transcript)].(msg.UserMessage)
	if !ok {
		t.Fatalf("appended message is %T, want msg.UserMessage", call.transcript[len(transcript)])
	}
	appendedText := msg.TextOf(appended.Content)
	if !strings.Contains(appendedText, "do not call any tool") {
		t.Errorf("appended message = %q, want it to instruct no tool calls", appendedText)
	}
	if !strings.Contains(appendedText, "## Goal") {
		t.Errorf("appended message = %q, want the structured summary format", appendedText)
	}

	if !strings.HasPrefix(result.Summary, wantSummary) {
		t.Errorf("Summary = %q, want it to start with the scripted summary text", result.Summary)
	}
	if result.TokensBefore != prep.TokensBefore {
		t.Errorf("TokensBefore = %d, want %d", result.TokensBefore, prep.TokensBefore)
	}
	if result.Usage != usage {
		t.Errorf("Usage = %+v, want %+v", result.Usage, usage)
	}
	if !reflect.DeepEqual(result.RetainedTail, prep.RetainedTail) {
		t.Errorf("RetainedTail = %+v, want prep.RetainedTail %+v (unchanged cut-point semantics)", result.RetainedTail, prep.RetainedTail)
	}

	if len(partDone) != 1 {
		t.Fatalf("OnPartDone called %d times, want 1", len(partDone))
	}
	if partDone[0].Path != "cache" {
		t.Errorf("PartDone.Path = %q, want %q", partDone[0].Path, "cache")
	}
	if partDone[0].CacheRead != usage.CacheRead {
		t.Errorf("PartDone.CacheRead = %d, want %d", partDone[0].CacheRead, usage.CacheRead)
	}
}

// TestCompactFastPathFallsBackOnToolCall asserts a model that calls a tool
// despite fastPathPrompt's instruction not to is treated as a failure of
// the cache path: CompactWith falls back to the serialize-and-split path
// and still returns a usable Result from the fallback's own response.
func TestCompactFastPathFallsBackOnToolCall(t *testing.T) {
	transcript, sysPrompt, tools := liveTranscriptFixture()
	prep := &Preparation{
		MessagesToSummarize: transcript,
		TokensBefore:        1,
		FileOps:             CreateFileOps(),
		Settings:            Settings{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 2000},
	}
	badCall := &msg.AssistantMessage{Role: msg.RoleAssistant, StopReason: msg.StopToolUse, Content: msg.Blocks{msg.NewToolCall("x", "bash", map[string]any{"command": "ls"})}}
	fallback := scriptedAssistant("## Goal\nfallback summary\n", msg.Usage{TotalTokens: 10})
	streamer := &scriptedStreamer{responses: []*msg.AssistantMessage{badCall, fallback}}

	in := FastPathInput{SystemPrompt: sysPrompt, Tools: tools, Transcript: transcript}
	result, err := CompactWith(context.Background(), prep, streamer, fastModel(), nil, provider.ThinkingOff, Options{FastPath: &in})
	if err != nil {
		t.Fatalf("CompactWith: %v", err)
	}
	if len(streamer.calls) != 2 {
		t.Fatalf("streamer called %d times, want 2 (the cache attempt, then the serialized fallback)", len(streamer.calls))
	}
	// The fallback call is the legacy serialized request: a single-message
	// transcript, SummarizationSystemPrompt, no tools.
	fb := streamer.calls[1]
	if len(fb.transcript) != 1 {
		t.Errorf("fallback transcript has %d messages, want 1 (the serialized path's one user message)", len(fb.transcript))
	}
	if fb.opts.SystemPrompt != SummarizationSystemPrompt {
		t.Errorf("fallback SystemPrompt = %q, want SummarizationSystemPrompt", fb.opts.SystemPrompt)
	}
	if len(fb.opts.Tools) != 0 {
		t.Errorf("fallback Tools = %+v, want none (the serialized path never offers tools)", fb.opts.Tools)
	}
	if !strings.HasPrefix(result.Summary, "## Goal\nfallback summary\n") {
		t.Errorf("Summary = %q, want the fallback's summary", result.Summary)
	}
}

// TestCompactFastPathFallsBackWhenItDoesNotFit asserts a FastPathInput too
// large for the model's window is never sent: CompactWith goes straight to
// the serialized path without attempting the cache one.
func TestCompactFastPathFallsBackWhenItDoesNotFit(t *testing.T) {
	huge := longHistory(200, 2000) // ~400k tokens of transcript
	prep := &Preparation{MessagesToSummarize: huge, Settings: Settings{Enabled: true, ReserveTokens: 2048, KeepRecentTokens: 2000}}
	streamer := &seqStreamer{}
	in := FastPathInput{SystemPrompt: "sys", Transcript: huge}

	if _, err := CompactWith(context.Background(), prep, streamer, smallModel(32768), nil, provider.ThinkingOff, Options{FastPath: &in}); err != nil {
		t.Fatalf("CompactWith: %v", err)
	}
	for _, c := range streamer.calls {
		if c.opts.SystemPrompt != SummarizationSystemPrompt {
			t.Errorf("a request used SystemPrompt %q, want every request to be the serialized path's (the cache input does not fit a 32k window)", c.opts.SystemPrompt)
		}
	}
}

// TestCompactFastPathOneRequestForSplitTurn asserts that when a split-turn
// Preparation's cache-friendly input fits, CompactWith sends one request
// (not the legacy path's separate history and turn-prefix requests).
func TestCompactFastPathOneRequestForSplitTurn(t *testing.T) {
	transcript, sysPrompt, tools := liveTranscriptFixture()
	prep := &Preparation{
		MessagesToSummarize: transcript[:1],
		TurnPrefixMessages:  transcript[1:3],
		RetainedTail:        transcript[3:],
		IsSplitTurn:         true,
		TokensBefore:        1,
		FileOps:             CreateFileOps(),
		Settings:            Settings{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 2000},
	}
	streamer := &fakeStreamer{response: scriptedAssistant("## Goal\none request\n", msg.Usage{TotalTokens: 5})}
	in := FastPathInput{SystemPrompt: sysPrompt, Tools: tools, Transcript: transcript}

	if _, err := CompactWith(context.Background(), prep, streamer, fastModel(), nil, provider.ThinkingOff, Options{FastPath: &in}); err != nil {
		t.Fatalf("CompactWith: %v", err)
	}
	if len(streamer.calls) != 1 {
		t.Fatalf("streamer called %d times for a split turn that fits the cache path, want 1", len(streamer.calls))
	}
}

// TestCompactFastPathDisabledByDefault asserts the zero Options (no
// FastPath set) never attempts the cache path: every existing caller and
// test that does not set it keeps today's serialize-and-split behaviour
// byte-for-byte.
func TestCompactFastPathDisabledByDefault(t *testing.T) {
	prep := &Preparation{MessagesToSummarize: longHistory(1, 50), Settings: Settings{ReserveTokens: 4000}}
	streamer := &fakeStreamer{response: scriptedAssistant("summary", msg.Usage{})}
	if _, err := Compact(context.Background(), prep, streamer, fastModel(), nil, provider.ThinkingOff); err != nil {
		t.Fatal(err)
	}
	if len(streamer.calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(streamer.calls))
	}
	if streamer.calls[0].opts.SystemPrompt != SummarizationSystemPrompt {
		t.Errorf("SystemPrompt = %q, want SummarizationSystemPrompt (fast path must not run with no FastPathInput)", streamer.calls[0].opts.SystemPrompt)
	}
}

// TestCompactFastPathStalled asserts a cache-path request that stalls is
// treated as a failure of path 1 (doc.go: "any failure... falls back"):
// CompactWith does not surface the stall itself but goes on to attempt
// the serialized path -- which, against a streamer that stalls every
// request, then surfaces its own stall, proving the fallback was actually
// attempted rather than the first stall short-circuiting everything.
func TestCompactFastPathStalled(t *testing.T) {
	transcript, sysPrompt, _ := liveTranscriptFixture()
	prep := &Preparation{MessagesToSummarize: transcript, TokensBefore: 1, FileOps: CreateFileOps(), Settings: Settings{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 2000}}
	blocked := &seqStreamer{block: true}
	in := FastPathInput{SystemPrompt: sysPrompt, Transcript: transcript}

	start := time.Now()
	_, err := CompactWith(context.Background(), prep, blocked, fastModel(), nil, provider.ThinkingOff, Options{
		FastPath:          &in,
		FirstEventTimeout: func(int) time.Duration { return 20 * time.Millisecond },
	})
	var ce *Error
	if !isCompactionError(err, &ce) || ce.Code != "stalled" {
		t.Fatalf("err = %v, want a stalled error from the serialized fallback", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("took %s, want both the cache attempt and its fallback to give up quickly", time.Since(start))
	}
	if len(blocked.calls) < 2 {
		t.Errorf("streamer called %d times, want at least 2: the cache attempt, then the serialized fallback it fell back to", len(blocked.calls))
	}
}

// TestCompactFastPathFallsBackOnLengthStop asserts a cache-path summary cut
// off at summaryOutputCap (StopLength) is not used: the serialized path,
// with its larger budget, writes the summary instead.
func TestCompactFastPathFallsBackOnLengthStop(t *testing.T) {
	transcript, sysPrompt, tools := liveTranscriptFixture()
	prep := &Preparation{
		MessagesToSummarize: transcript,
		TokensBefore:        1,
		FileOps:             CreateFileOps(),
		Settings:            Settings{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 2000},
	}
	cut := scriptedAssistant("## Goal\nhalf a summ", msg.Usage{TotalTokens: 10})
	cut.StopReason = msg.StopLength
	whole := scriptedAssistant("## Goal\nwhole summary\n", msg.Usage{TotalTokens: 10})
	streamer := &scriptedStreamer{responses: []*msg.AssistantMessage{cut, whole}}

	in := FastPathInput{SystemPrompt: sysPrompt, Tools: tools, Transcript: transcript}
	result, err := CompactWith(context.Background(), prep, streamer, fastModel(), nil, provider.ThinkingOff, Options{FastPath: &in})
	if err != nil {
		t.Fatalf("CompactWith: %v", err)
	}
	if len(streamer.calls) != 2 {
		t.Fatalf("streamer called %d times, want 2 (the cut-off cache attempt, then the serialized path)", len(streamer.calls))
	}
	if got, cache := streamer.calls[1].opts.MaxTokens, streamer.calls[0].opts.MaxTokens; got <= cache {
		t.Errorf("serialized MaxTokens = %d, want more than the cache path's %d", got, cache)
	}
	if strings.Contains(result.Summary, "half a summ") || !strings.HasPrefix(result.Summary, "## Goal\nwhole summary\n") {
		t.Errorf("Summary = %q, want the serialized path's whole summary", result.Summary)
	}
}
