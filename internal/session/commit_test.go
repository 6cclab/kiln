package session

import (
	"testing"

	"github.com/andrepato/harness/internal/msg"
)

func TestPrepareCommitAssignsMonotonicSeqAndTimestamp(t *testing.T) {
	writes := []Write{
		EntryWrite{Entry: Entry{ID: "e1", Type: EntryMessage}},
		EntryWrite{Entry: Entry{ID: "e2", ParentID: strPtr("e1"), Type: EntryMessage}},
		UsageWrite{Row: UsageRow{ID: "u1", Usage: msg.Usage{}}},
	}
	committed, result := PrepareCommit(writes, 5, 1000)

	if result.FirstSeq != 5 {
		t.Fatalf("FirstSeq = %d, want 5", result.FirstSeq)
	}
	wantSeqs := []int64{5, 6, 7}
	for i, s := range wantSeqs {
		if result.Seqs[i] != s {
			t.Fatalf("Seqs[%d] = %d, want %d", i, result.Seqs[i], s)
		}
	}
	if result.Timestamp != 1000 {
		t.Fatalf("Timestamp = %d, want 1000", result.Timestamp)
	}

	if committed[0].Entry.Seq != 5 || committed[0].Entry.Timestamp != 1000 {
		t.Fatalf("committed[0] entry = %+v, want seq=5 timestamp=1000", committed[0].Entry)
	}
	if committed[1].Entry.Seq != 6 {
		t.Fatalf("committed[1] entry seq = %d, want 6", committed[1].Entry.Seq)
	}
	if committed[2].Usage.Seq != 7 {
		t.Fatalf("committed[2] usage seq = %d, want 7", committed[2].Usage.Seq)
	}
	// Timestamp is not assigned to usage/value writes, only entries.
	if committed[2].Usage.Seq != 7 {
		t.Fatalf("usage seq mismatch")
	}
}

func TestPrepareCommitValueWrite(t *testing.T) {
	vw := ValueWrite{Kind: "value", Op: "set", Namespace: "ns", Key: "k", Value: []byte(`1`)}
	committed, result := PrepareCommit([]Write{vw}, 10, 42)
	if result.Seqs[0] != 10 {
		t.Fatalf("Seqs[0] = %d, want 10", result.Seqs[0])
	}
	if committed[0].Kind != "value" || committed[0].Value.Seq != 10 {
		t.Fatalf("committed[0] = %+v, want kind=value seq=10", committed[0])
	}
}

// fakeExistingState is a minimal existingState for ValidateCommittedWrites
// tests that need prior-state lookups independent of a real *State.
type fakeExistingState struct {
	entryUsageIDs map[string]bool
	entryIDs      map[string]bool
}

func (f fakeExistingState) hasEntryOrUsageID(id string) bool { return f.entryUsageIDs[id] }
func (f fakeExistingState) hasEntryID(id string) bool        { return f.entryIDs[id] }

func TestValidateCommittedWritesValidCase(t *testing.T) {
	writes := []Write{
		EntryWrite{Entry: Entry{ID: "e1", Type: EntryMessage}},
		EntryWrite{Entry: Entry{ID: "e2", ParentID: strPtr("e1"), Type: EntryMessage}},
	}
	committed, _ := PrepareCommit(writes, 1, 0)
	state := fakeExistingState{entryUsageIDs: map[string]bool{}, entryIDs: map[string]bool{}}
	if err := ValidateCommittedWrites(committed, 1, state); err != nil {
		t.Fatalf("ValidateCommittedWrites: %v", err)
	}
}

func TestValidateCommittedWritesDuplicateWithinBatch(t *testing.T) {
	writes := []Write{
		EntryWrite{Entry: Entry{ID: "e1", Type: EntryMessage}},
		EntryWrite{Entry: Entry{ID: "e1", Type: EntryMessage}},
	}
	committed, _ := PrepareCommit(writes, 1, 0)
	state := fakeExistingState{entryUsageIDs: map[string]bool{}, entryIDs: map[string]bool{}}
	err := ValidateCommittedWrites(committed, 1, state)
	if err == nil {
		t.Fatalf("ValidateCommittedWrites duplicate id: got nil error, want error")
	}
}

func TestValidateCommittedWritesDuplicateAgainstPriorState(t *testing.T) {
	writes := []Write{
		UsageWrite{Row: UsageRow{ID: "u1"}},
	}
	committed, _ := PrepareCommit(writes, 1, 0)
	state := fakeExistingState{entryUsageIDs: map[string]bool{"u1": true}, entryIDs: map[string]bool{}}
	err := ValidateCommittedWrites(committed, 1, state)
	if err == nil {
		t.Fatalf("ValidateCommittedWrites duplicate against prior state: got nil error, want error")
	}
}

func TestValidateCommittedWritesMissingParentEntry(t *testing.T) {
	writes := []Write{
		EntryWrite{Entry: Entry{ID: "e2", ParentID: strPtr("nonexistent"), Type: EntryMessage}},
	}
	committed, _ := PrepareCommit(writes, 1, 0)
	state := fakeExistingState{entryUsageIDs: map[string]bool{}, entryIDs: map[string]bool{}}
	err := ValidateCommittedWrites(committed, 1, state)
	if err == nil {
		t.Fatalf("ValidateCommittedWrites missing parent: got nil error, want error")
	}
}

func TestValidateCommittedWritesParentAddedEarlierInSameBatch(t *testing.T) {
	writes := []Write{
		EntryWrite{Entry: Entry{ID: "e1", Type: EntryMessage}},
		EntryWrite{Entry: Entry{ID: "e2", ParentID: strPtr("e1"), Type: EntryMessage}},
	}
	committed, _ := PrepareCommit(writes, 1, 0)
	state := fakeExistingState{entryUsageIDs: map[string]bool{}, entryIDs: map[string]bool{}}
	if err := ValidateCommittedWrites(committed, 1, state); err != nil {
		t.Fatalf("ValidateCommittedWrites parent added earlier in batch: %v", err)
	}
}

func TestValidateCommittedWritesParentFromPriorState(t *testing.T) {
	writes := []Write{
		EntryWrite{Entry: Entry{ID: "e2", ParentID: strPtr("e1"), Type: EntryMessage}},
	}
	committed, _ := PrepareCommit(writes, 1, 0)
	state := fakeExistingState{entryUsageIDs: map[string]bool{"e1": true}, entryIDs: map[string]bool{"e1": true}}
	if err := ValidateCommittedWrites(committed, 1, state); err != nil {
		t.Fatalf("ValidateCommittedWrites parent from prior state: %v", err)
	}
}

func TestValidateCommittedWritesNonMonotonicSeq(t *testing.T) {
	// Build committed writes by hand with a seq that does not exceed
	// firstSeq-1.
	e := Entry{ID: "e1", Seq: 0, Type: EntryMessage}
	committed := []CommittedWrite{{Kind: "entry", Entry: &e}}
	state := fakeExistingState{entryUsageIDs: map[string]bool{}, entryIDs: map[string]bool{}}
	err := ValidateCommittedWrites(committed, 1, state)
	if err == nil {
		t.Fatalf("ValidateCommittedWrites non-monotonic seq: got nil error, want error")
	}
}

func TestValidateCommittedWritesNonMonotonicAcrossWrites(t *testing.T) {
	e1 := Entry{ID: "e1", Seq: 5, Type: EntryMessage}
	e2 := Entry{ID: "e2", Seq: 5, ParentID: strPtr("e1"), Type: EntryMessage}
	committed := []CommittedWrite{
		{Kind: "entry", Entry: &e1},
		{Kind: "entry", Entry: &e2},
	}
	state := fakeExistingState{entryUsageIDs: map[string]bool{}, entryIDs: map[string]bool{}}
	err := ValidateCommittedWrites(committed, 5, state)
	if err == nil {
		t.Fatalf("ValidateCommittedWrites equal-seq second write: got nil error, want error")
	}
}
