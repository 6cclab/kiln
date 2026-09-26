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

	out := RenderTranscriptEntries([]session.Entry{entryA, entryB}, 80, false, "/tmp", synthetics)
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

	out := RenderTranscriptEntries([]session.Entry{entryA}, 80, false, "/tmp", synthetics)
	joined := strings.Join(out, "\n")

	idxNote := strings.Index(joined, "before anything")
	idxA := strings.Index(joined, "first message")
	if idxNote < 0 || idxA < 0 || idxNote >= idxA {
		t.Fatalf("expected the pre-first-entry synthetic ahead of entry A; got:\n%s", joined)
	}
}
