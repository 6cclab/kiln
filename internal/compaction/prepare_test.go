package compaction

import (
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// TestPrepareNotApplicable ports pi's prepareCompaction "not applicable"
// cases: an empty path, and a path whose tip is already a compaction
// entry.
func TestPrepareNotApplicable(t *testing.T) {
	prep, err := Prepare(nil, DefaultSettings)
	if err != nil || prep != nil {
		t.Fatalf("Prepare(nil, ...) = %+v, %v, want nil, nil", prep, err)
	}

	entries := []session.Entry{
		{ID: "c0", Type: session.EntryCompaction, Summary: "already compacted"},
	}
	prep, err = Prepare(entries, DefaultSettings)
	if err != nil || prep != nil {
		t.Fatalf("Prepare(tip-is-compaction, ...) = %+v, %v, want nil, nil", prep, err)
	}
}

// TestPrepareOnLargeFixtureKeepEverything asks Prepare to keep far more
// tokens than the whole branch represents: nothing should be summarized,
// the entire branch should come back as RetainedTail, and FirstKeptEntryID
// should be the branch's first (oldest) entry.
func TestPrepareOnLargeFixtureKeepEverything(t *testing.T) {
	entries := loadMainBranch(t, largeFixture)

	settings := Settings{Enabled: true, ReserveTokens: 1000, KeepRecentTokens: 1_000_000}
	prep, err := Prepare(entries, settings)
	if err != nil {
		t.Fatal(err)
	}
	if prep == nil {
		t.Fatal("Prepare returned nil, nil; want a Preparation")
	}
	if len(prep.MessagesToSummarize) != 0 {
		t.Fatalf("MessagesToSummarize = %d entries, want 0", len(prep.MessagesToSummarize))
	}
	if prep.IsSplitTurn {
		t.Fatal("IsSplitTurn = true, want false (the whole single turn is kept)")
	}
	if len(prep.RetainedTail) != len(entries) {
		t.Fatalf("RetainedTail = %d messages, want %d (every entry)", len(prep.RetainedTail), len(entries))
	}
	if prep.FirstKeptEntryID != entries[0].ID {
		t.Fatalf("FirstKeptEntryID = %q, want %q (the oldest entry)", prep.FirstKeptEntryID, entries[0].ID)
	}
	wantTokens := CalculateContextTokens(entries).Tokens
	if prep.TokensBefore != wantTokens {
		t.Fatalf("TokensBefore = %d, want %d", prep.TokensBefore, wantTokens)
	}
	if prep.PreviousSummary != nil {
		t.Fatalf("PreviousSummary = %v, want nil (no prior compaction on this path)", prep.PreviousSummary)
	}
}

// TestPrepareOnLargeFixtureSplitsTheOnlyTurn asks Prepare to keep almost
// nothing. The fixture's branch is a single turn (one user message,
// followed by four assistant/toolResult pairs), so the only way to keep
// close to nothing is to split that turn: the last assistant reply is
// retained, everything before it in the same turn becomes
// TurnPrefixMessages, and MessagesToSummarize (true prior-turn history) is
// empty because there is no earlier turn.
func TestPrepareOnLargeFixtureSplitsTheOnlyTurn(t *testing.T) {
	entries := loadMainBranch(t, largeFixture)

	settings := Settings{Enabled: true, ReserveTokens: 1000, KeepRecentTokens: 1}
	prep, err := Prepare(entries, settings)
	if err != nil {
		t.Fatal(err)
	}
	if prep == nil {
		t.Fatal("Prepare returned nil, nil; want a Preparation")
	}
	if !prep.IsSplitTurn {
		t.Fatal("IsSplitTurn = false, want true (keepRecentTokens=1 forces a mid-turn cut)")
	}
	if len(prep.MessagesToSummarize) != 0 {
		t.Fatalf("MessagesToSummarize = %d messages, want 0 (there is no earlier turn to summarize)", len(prep.MessagesToSummarize))
	}
	if len(prep.TurnPrefixMessages) == 0 {
		t.Fatal("TurnPrefixMessages is empty, want the split-off prefix of the only turn")
	}
	if len(prep.RetainedTail) == 0 {
		t.Fatal("RetainedTail is empty, want at least the final assistant reply")
	}
	lastID := entries[len(entries)-1].ID
	if prep.FirstKeptEntryID != lastID {
		t.Fatalf("FirstKeptEntryID = %q, want %q (the final assistant reply)", prep.FirstKeptEntryID, lastID)
	}
	// The turn's prefix reads zz_secret.txt (via a "read" tool call and a
	// "bash" tool call); extractFileOperations only recognizes the "read"
	// tool by name, so it should show up as a read file.
	if _, ok := prep.FileOps.Read["zz_secret.txt"]; !ok {
		t.Fatalf("FileOps.Read = %+v, want it to contain zz_secret.txt", prep.FileOps.Read)
	}
}

// TestPrepareIterative simulates a second compaction on top of a first: a
// synthetic path whose first entry is already a session.EntryCompaction
// (with its own RetainedTail and Details), followed by new activity. The
// prior compaction's RetainedTail must be walked as if it were live
// entries (pi's virtualRetainedEntries), its Summary must come back as
// PreviousSummary, and its Details' file lists must seed FileOps.
func TestPrepareIterative(t *testing.T) {
	priorDetails := `{"readFiles":["old.go"],"modifiedFiles":["main.go"]}`
	compactionEntry := session.Entry{
		ID:           "c0",
		Type:         session.EntryCompaction,
		Summary:      "prior summary text",
		TokensBefore: 999,
		Details:      []byte(priorDetails),
		RetainedTail: retainedTailFixture(),
	}
	path := append([]session.Entry{compactionEntry}, newTurnAfterCompaction()...)

	settings := Settings{Enabled: true, ReserveTokens: 1000, KeepRecentTokens: 1_000_000}
	prep, err := Prepare(path, settings)
	if err != nil {
		t.Fatal(err)
	}
	if prep == nil {
		t.Fatal("Prepare returned nil, nil; want a Preparation")
	}
	if prep.PreviousSummary == nil || *prep.PreviousSummary != "prior summary text" {
		t.Fatalf("PreviousSummary = %v, want %q", prep.PreviousSummary, "prior summary text")
	}
	if _, ok := prep.FileOps.Read["old.go"]; !ok {
		t.Fatalf("FileOps.Read = %+v, want it seeded with old.go from the prior compaction's Details", prep.FileOps.Read)
	}
	if _, ok := prep.FileOps.Edited["main.go"]; !ok {
		t.Fatalf("FileOps.Edited = %+v, want it seeded with main.go from the prior compaction's Details", prep.FileOps.Edited)
	}
}

// retainedTailFixture is a small retained tail for TestPrepareIterative: a
// user message followed by an assistant reply, as a prior compaction would
// have stored it.
func retainedTailFixture() []msg.Message {
	return []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("what's next")}, Timestamp: 1},
		msg.AssistantMessage{Role: msg.RoleAssistant, StopReason: msg.StopStop, Content: msg.Blocks{msg.Text("let's continue")}, Timestamp: 2},
	}
}

// newTurnAfterCompaction is one new turn following a prior compaction, for
// TestPrepareIterative.
func newTurnAfterCompaction() []session.Entry {
	return []session.Entry{
		userEntry("u-new", "one more thing"),
		assistantTextEntry("a-new", "done"),
	}
}
