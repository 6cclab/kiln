package tui

import (
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// TestRenderTranscriptEntries_SplicesSynthetics is the unit-level proof for
// Bridge.CommitSynthetic/RenderTranscriptEntries's splice: a block
// committed with no session entry of its own (a plan checklist, a
// subagent line, a CommitNote, a /context block) must land back in the
// same position on a Ctrl+O/Ctrl+F replay, not silently vanish because
// RenderTranscriptEntries otherwise only knows about entries actually
// written to the session log (see SyntheticCommit's doc comment in
// bridge.go). This drives RenderTranscriptEntries directly, the same
// function app.go's replayTranscript calls, with a synthetic tagged to
// land between two entries: commit entry A, a note, entry B — replay must
// yield A, then the note, then B, in that order.
func TestRenderTranscriptEntries_SplicesSynthetics(t *testing.T) {
	entryA := session.Entry{
		ID:   "entry-a",
		Type: session.EntryMessage,
		Message: msg.UserMessage{
			Role:    msg.RoleUser,
			Content: msg.Blocks{msg.Text("first message")},
		},
	}
	parentA := entryA.ID
	entryB := session.Entry{
		ID:       "entry-b",
		ParentID: &parentA,
		Type:     session.EntryMessage,
		Message: msg.UserMessage{
			Role:    msg.RoleUser,
			Content: msg.Blocks{msg.Text("second message")},
		},
	}

	noteLines := RenderNote("a system note", 80)
	synthetics := []SyntheticCommit{
		{AfterEntryID: entryA.ID, Lines: noteLines},
	}

	out := RenderTranscriptEntries([]session.Entry{entryA, entryB}, 80, false, "/tmp", synthetics, nil)
	joined := strings.Join(out, "\n")

	idxA := strings.Index(joined, "first message")
	idxNote := strings.Index(joined, "a system note")
	idxB := strings.Index(joined, "second message")

	if idxA < 0 || idxNote < 0 || idxB < 0 {
		t.Fatalf("expected all three blocks in the replayed transcript; got:\n%s", joined)
	}
	if !(idxA < idxNote && idxNote < idxB) {
		t.Fatalf("expected order A, note, B; got indices A=%d note=%d B=%d in:\n%s", idxA, idxNote, idxB, joined)
	}
}

// TestRenderTranscriptEntries_SyntheticBeforeFirstEntry checks the ""
// AfterEntryID case (a synthetic committed before any entry exists yet)
// lands ahead of every entry.
func TestRenderTranscriptEntries_SyntheticBeforeFirstEntry(t *testing.T) {
	entryA := session.Entry{
		ID:   "entry-a",
		Type: session.EntryMessage,
		Message: msg.UserMessage{
			Role:    msg.RoleUser,
			Content: msg.Blocks{msg.Text("first message")},
		},
	}
	synthetics := []SyntheticCommit{
		{AfterEntryID: "", Lines: RenderNote("before anything", 80)},
	}

	out := RenderTranscriptEntries([]session.Entry{entryA}, 80, false, "/tmp", synthetics, nil)
	joined := strings.Join(out, "\n")

	idxNote := strings.Index(joined, "before anything")
	idxA := strings.Index(joined, "first message")
	if idxNote < 0 || idxA < 0 || idxNote >= idxA {
		t.Fatalf("expected the pre-first-entry synthetic ahead of entry A; got:\n%s", joined)
	}
}

// TestSyntheticCommit_RenderRebuildsAtNewWidth checks a "system" note's
// synthetic commit redraws its label rule at a resize's new width instead
// of replaying the literal text it was committed with — the label rule
// (and the right margin every other block keeps) otherwise stayed at
// whatever width was live when the note first committed, through every
// later resize (qa/findings/20261004T204953Z-system-blocks-not-reflowed-
// on-resize.json). Bridge.commitNote supplies Rebuild for exactly this.
func TestSyntheticCommit_RenderRebuildsAtNewWidth(t *testing.T) {
	committedAt80 := RenderNote("■ Interrupted. Tell kiln what to do instead.", 80)
	sc := SyntheticCommit{
		Lines: committedAt80,
		Rebuild: func(w int) []string {
			return RenderNote("■ Interrupted. Tell kiln what to do instead.", w)
		},
	}

	at160 := sc.Render(160)
	if strings.Join(at160, "\n") == strings.Join(committedAt80, "\n") {
		t.Fatalf("Render(160) returned the width-80 text unchanged; the rule must redraw wider")
	}
	wantRule := labelRule("system", Muted, "", 160)
	if at160[0] != wantRule {
		t.Errorf("Render(160)[0] = %q, want the width-160 label rule %q", at160[0], wantRule)
	}

	// Without a Rebuild func, Render falls back to the stored Lines — the
	// pre-fix behaviour, still correct for a caller with nothing cheap to
	// recompute from.
	plain := SyntheticCommit{Lines: committedAt80}
	if got := plain.Render(160); strings.Join(got, "\n") != strings.Join(committedAt80, "\n") {
		t.Errorf("Render with no Rebuild changed the lines: got %v, want the stored Lines unchanged", got)
	}
}

// TestRenderTranscriptEntries_OneBlankRowBetweenBlocks: a replay (ctrl+o,
// resume) separates blocks by one blank row, as the live view does; the
// verbose view used to show two between every block.
func TestRenderTranscriptEntries_OneBlankRowBetweenBlocks(t *testing.T) {
	call := msg.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": "/tmp/p/a.txt"}}
	entries := []session.Entry{
		{ID: "1", Type: session.EntryMessage, Message: msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("read it")}}},
		{ID: "2", Type: session.EntryMessage, Message: msg.AssistantMessage{Role: msg.RoleAssistant, Content: msg.Blocks{msg.Text("Reading."), call}}},
		{ID: "3", Type: session.EntryMessage, Message: msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: "c1", ToolName: "read", Content: msg.Blocks{msg.Text("hello")}}},
		{ID: "4", Type: session.EntryMessage, Message: msg.AssistantMessage{Role: msg.RoleAssistant, Content: msg.Blocks{msg.Text("Done.")}}},
	}
	for _, verbose := range []bool{false, true} {
		out := RenderTranscriptEntries(entries, 80, verbose, "/tmp/p", nil, nil)
		for i := 1; i < len(out); i++ {
			if strings.TrimSpace(stripANSI(out[i])) == "" && strings.TrimSpace(stripANSI(out[i-1])) == "" {
				t.Errorf("verbose=%v: two blank rows at %d:\n%s", verbose, i, strings.Join(out, "\n"))
				break
			}
		}
	}
}

// TestRenderTranscriptEntries_SkipsTaskCallsThePanelCovers: a task
// dispatch the subagents panel showed gets no task block of its own live,
// and a replay (Ctrl+F, Ctrl+O) must not bring one back beside the
// replayed panel; verbose still shows it, as live.
func TestRenderTranscriptEntries_SkipsTaskCallsThePanelCovers(t *testing.T) {
	call := msg.ToolCall{ID: "t1", Name: "task", Arguments: map[string]any{"description": "check the auth middleware"}}
	entries := []session.Entry{
		{ID: "1", Type: session.EntryMessage, Message: msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("scout")}}},
		{ID: "2", Type: session.EntryMessage, Message: msg.AssistantMessage{Role: msg.RoleAssistant, Content: msg.Blocks{call}}},
		{ID: "3", Type: session.EntryMessage, Message: msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: "t1", ToolName: "task", Content: msg.Blocks{msg.Text("auth checks for an Authorization header")}}},
	}
	covered := map[string]bool{"t1": true}
	panel := []SyntheticCommit{{AfterEntryID: "3", Lines: []string{"", "PANEL ROW"}}}
	out := stripANSI(strings.Join(RenderTranscriptEntries(entries, 80, false, "/tmp/p", panel, covered), "\n"))
	if strings.Contains(out, "Authorization header") {
		t.Errorf("a covered task call replayed as its own block:\n%s", out)
	}
	if !strings.Contains(out, "PANEL ROW") {
		t.Errorf("the panel anchored to the skipped entry was dropped too:\n%s", out)
	}
	if out := stripANSI(strings.Join(RenderTranscriptEntries(entries, 80, true, "/tmp/p", nil, covered), "\n")); !strings.Contains(out, "Authorization header") {
		t.Errorf("verbose replay dropped the task block:\n%s", out)
	}
	if out := stripANSI(strings.Join(RenderTranscriptEntries(entries, 80, false, "/tmp/p", nil, nil), "\n")); !strings.Contains(out, "Authorization header") {
		t.Errorf("an uncovered task call (a resumed session, no panel) was dropped:\n%s", out)
	}
}
