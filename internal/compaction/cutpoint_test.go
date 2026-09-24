package compaction

import (
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// userEntry, assistantToolCallEntry, toolResultEntry and assistantTextEntry
// build small synthetic session.Entry values for cut-point tests: enough
// content to control EstimateTokens without needing a real fixture.
func userEntry(id string, text string) session.Entry {
	return session.Entry{
		ID:      id,
		Type:    session.EntryMessage,
		Message: msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text(text)}},
	}
}

func assistantToolCallEntry(id, toolCallID, text string) session.Entry {
	return session.Entry{
		ID:   id,
		Type: session.EntryMessage,
		Message: msg.AssistantMessage{
			Role:       msg.RoleAssistant,
			StopReason: msg.StopToolUse,
			Content: msg.Blocks{
				msg.Text(text),
				msg.NewToolCall(toolCallID, "read", map[string]any{"path": "a.go"}),
			},
		},
	}
}

func assistantTextEntry(id, text string) session.Entry {
	return session.Entry{
		ID:   id,
		Type: session.EntryMessage,
		Message: msg.AssistantMessage{
			Role:       msg.RoleAssistant,
			StopReason: msg.StopStop,
			Content:    msg.Blocks{msg.Text(text)},
		},
	}
}

func toolResultEntry(id, toolCallID, text string) session.Entry {
	return session.Entry{
		ID:   id,
		Type: session.EntryMessage,
		Message: msg.ToolResultMessage{
			Role:       msg.RoleToolResult,
			ToolCallID: toolCallID,
			Content:    msg.Blocks{msg.Text(text)},
		},
	}
}

// turnTranscript builds a synthetic transcript of N complete turns, each
// turn being [user, assistant-tool-call, tool-result, assistant-text]. Text
// lengths are chosen (60 chars => 15 tokens each block) so token math is
// easy to reason about in assertions.
func turnTranscript(turns int) []session.Entry {
	pad := "0123456789012345678901234567890123456789012345678901234567890" // 61 chars ~ 16 tokens
	var entries []session.Entry
	for i := 0; i < turns; i++ {
		base := i * 4
		entries = append(entries,
			userEntry(idx(base), pad),
			assistantToolCallEntry(idx(base+1), idx(base+1)+"-call", pad),
			toolResultEntry(idx(base+2), idx(base+1)+"-call", pad),
			assistantTextEntry(idx(base+3), pad),
		)
	}
	return entries
}

func idx(i int) string {
	return "e" + string(rune('a'+i%26)) + string(rune('0'+(i/26)%10))
}

// TestFindCutPointNeverSplitsToolCallFromResult asserts, across every
// keepRecentTokens value from 0 up to well past the whole transcript's
// size, that FindCutPoint never lands on the toolResult entry that follows
// an assistant tool call: cutting there would strand a tool result whose
// tool call was summarized away, which the model reading it back would not
// be able to make sense of.
func TestFindCutPointNeverSplitsToolCallFromResult(t *testing.T) {
	entries := turnTranscript(6)
	totalTokens := 0
	for _, e := range entries {
		totalTokens += EstimateTokens(e.Message)
	}

	for keep := 0; keep <= totalTokens+50; keep += 5 {
		cp := FindCutPoint(entries, 0, len(entries), keep)
		if cp.CutIndex < 0 || cp.CutIndex >= len(entries) {
			continue // cut at/after the end is fine; nothing to check
		}
		cutEntry := entries[cp.CutIndex]
		if tr, ok := cutEntry.Message.(msg.ToolResultMessage); ok {
			t.Fatalf("keepRecentTokens=%d: cut landed on toolResult %s (id=%s), which strands its tool call", keep, tr.ToolCallID, cutEntry.ID)
		}
	}
}

// TestFindCutPointHonoursKeepRecent asserts the walk-back loop's core
// contract: it accumulates tokens from the end until it reaches
// keepRecentTokens, then cuts at the first valid cut point at-or-after that
// boundary. When that boundary entry is itself a valid cut point (a user or
// assistant message, as here: keep=50 lands the boundary on an assistant
// tool-call entry), the retained tail's estimated tokens are exactly the
// accumulated sum at that point, which is always >= keepRecentTokens by
// construction.
//
// This is not a universal >= guarantee: when the boundary lands on a
// toolResult (never a valid cut point), the cut snaps forward to the next
// valid entry, which can drop below keepRecentTokens -- see
// TestFindCutPointNeverSplitsToolCallFromResult's keep=100 case in the
// debug trace this test's constants were derived from (turnTranscript(6),
// keep=100 keeps only 84 tokens). Turn-safety, not a token-count promise, is
// the guarantee FindCutPoint actually gives; this test checks the case
// where both hold.
func TestFindCutPointHonoursKeepRecent(t *testing.T) {
	entries := turnTranscript(6)

	const keep = 50
	cp := FindCutPoint(entries, 0, len(entries), keep)

	kept := 0
	for i := cp.CutIndex; i < len(entries); i++ {
		kept += EstimateTokens(entries[i].Message)
	}
	if kept < keep {
		t.Fatalf("kept tail tokens = %d, want >= keepRecentTokens (%d)", kept, keep)
	}
	if cp.CutIndex != 21 {
		t.Fatalf("CutIndex = %d, want 21 (the last turn's assistant tool-call entry)", cp.CutIndex)
	}

	// The cut must land on a valid cut point: a user or assistant message,
	// never a toolResult.
	cutEntry := entries[cp.CutIndex]
	if _, ok := cutEntry.Message.(msg.ToolResultMessage); ok {
		t.Fatalf("cut landed on a toolResult entry: %s", cutEntry.ID)
	}
}

// TestFindCutPointNoValidCutPoints asserts the documented fallback when the
// range has no valid cut point at all (e.g. it is entirely toolResult
// entries): CutIndex is startIndex, TurnStartIndex is -1, IsSplitTurn is
// false.
func TestFindCutPointNoValidCutPoints(t *testing.T) {
	entries := []session.Entry{
		toolResultEntry("r0", "call-0", "x"),
		toolResultEntry("r1", "call-1", "y"),
	}
	cp := FindCutPoint(entries, 0, len(entries), 1000)
	if cp.CutIndex != 0 || cp.TurnStartIndex != -1 || cp.IsSplitTurn {
		t.Fatalf("got %+v, want {CutIndex:0 TurnStartIndex:-1 IsSplitTurn:false}", cp)
	}
}

// TestFindCutPointSplitTurn forces a cut inside the final turn by asking
// to keep only its last entry (its assistant-text reply). The walk-back
// loop reaches keepRecentTokens right at that last entry, which is itself
// a valid cut point (an assistant message), so CutIndex lands there
// directly -- but that entry is not the turn's leading user message, so
// the cut splits the turn: IsSplitTurn is true and TurnStartIndex points
// back at the turn's user entry.
func TestFindCutPointSplitTurn(t *testing.T) {
	entries := turnTranscript(2)
	cp := FindCutPoint(entries, 0, len(entries), 16)

	finalTurnStart := 4 // index of the second turn's user entry
	lastEntry := len(entries) - 1
	if cp.CutIndex != lastEntry {
		t.Fatalf("CutIndex = %d, want %d (the final turn's assistant-text entry)", cp.CutIndex, lastEntry)
	}
	if !cp.IsSplitTurn {
		t.Fatal("expected IsSplitTurn = true")
	}
	if cp.TurnStartIndex != finalTurnStart {
		t.Fatalf("TurnStartIndex = %d, want %d", cp.TurnStartIndex, finalTurnStart)
	}
}

// TestFindCutPointPullsBranchSummaryIntoRetained asserts that a
// branch_summary entry sitting immediately before the walk-back loop's
// initial cut choice is pulled into the retained side (CutIndex moves back
// onto it), matching pi's "absorb preceding branch_summary/custom entries"
// loop: a branch summary is never left dangling in the summarized-away
// portion, immediately followed on the retained side by the turn it was
// recapping.
//
// entries: [u0("hello", 2 tok), bs0(branch_summary, 0 tok -- not a message,
// contributes nothing to the walk-back accumulation), u1("continue",
// 2 tok), a1("ok", 1 tok)]. With keepRecentTokens=2, the walk-back loop
// reaches 2 accumulated tokens at u1 (a1's 1 token, then u1's 2 more =
// 3 >= 2), and u1 is itself a valid cut point, so the loop's initial pick
// is CutIndex=3 (u1). The entry immediately before that, bs0, is a
// branch_summary, so the pull-back loop absorbs it: CutIndex moves to 2
// (bs0 itself). a0 before bs0 is a message, which stops the pull-back
// there.
func TestFindCutPointPullsBranchSummaryIntoRetained(t *testing.T) {
	entries := []session.Entry{
		userEntry("u0", "hello"),
		assistantTextEntry("a0", "hi"),
		{ID: "bs0", Type: session.EntryBranchSummary, Summary: "branch recap"},
		userEntry("u1", "continue"),
		assistantTextEntry("a1", "ok"),
	}
	cp := FindCutPoint(entries, 0, len(entries), 2)
	if cp.CutIndex != 2 {
		t.Fatalf("CutIndex = %d, want 2 (bs0, pulled back from u1)", cp.CutIndex)
	}
	if entries[cp.CutIndex].Type != session.EntryBranchSummary {
		t.Fatalf("entry at CutIndex is %s, want branch_summary", entries[cp.CutIndex].Type)
	}
}

func TestFindTurnStartIndex(t *testing.T) {
	entries := []session.Entry{
		userEntry("u0", "hello"),
		assistantToolCallEntry("a0", "a0-call", "work"),
		toolResultEntry("t0", "a0-call", "result"),
		assistantTextEntry("a1", "done"),
	}
	got := FindTurnStartIndex(entries, 3, 0)
	if got != 0 {
		t.Fatalf("FindTurnStartIndex = %d, want 0", got)
	}
}
