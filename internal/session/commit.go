package session

import "fmt"

// PrepareCommit assigns seq (starting at firstSeq, incrementing by one per
// write, in order) and timestamp to a batch of uncommitted writes. It
// mirrors prepareStorageCommit in commit.js.
func PrepareCommit(writes []Write, firstSeq int64, timestamp int64) ([]CommittedWrite, CommitResult) {
	out := make([]CommittedWrite, len(writes))
	seqs := make([]int64, len(writes))
	for i, w := range writes {
		seq := firstSeq + int64(i)
		seqs[i] = seq
		switch v := w.(type) {
		case EntryWrite:
			e := v.Entry
			e.Seq = seq
			e.Timestamp = timestamp
			out[i] = CommittedWrite{Kind: "entry", Entry: &e}
		case UsageWrite:
			r := v.Row
			r.Seq = seq
			out[i] = CommittedWrite{Kind: "usage", Usage: &r}
		case ValueWrite:
			vw := v
			vw.Seq = seq
			out[i] = CommittedWrite{Kind: vw.Kind, Value: &vw}
		default:
			panic(fmt.Sprintf("session: unknown write type %T", w))
		}
	}
	return out, CommitResult{FirstSeq: firstSeq, Seqs: seqs, Timestamp: timestamp}
}

// existingState is the minimal lookup surface ValidateCommittedWrites needs
// from prior storage state.
type existingState interface {
	hasEntryOrUsageID(id string) bool
	hasEntryID(id string) bool
}

// ValidateCommittedWrites enforces monotonic seq (must exceed firstSeq-1),
// unique entry/usage ids (within the batch and against prior state), and
// that every non-root entry's parent already exists (in prior state or
// earlier in this same batch). It mirrors validateCommittedWrites in
// commit.js.
func ValidateCommittedWrites(writes []CommittedWrite, firstSeq int64, state existingState) error {
	previousSeq := firstSeq - 1
	transactionIDs := map[string]bool{}
	transactionEntryIDs := map[string]bool{}
	for _, w := range writes {
		seq := w.Seq()
		if seq <= previousSeq {
			return fmt.Errorf("session: non-monotonic storage sequence: %d", seq)
		}
		previousSeq = seq
		if w.Kind != "entry" && w.Kind != "usage" {
			continue
		}
		var id, parentID string
		var hasParent, isEntry bool
		if w.Kind == "entry" {
			id = w.Entry.ID
			isEntry = true
			if w.Entry.ParentID != nil {
				parentID = *w.Entry.ParentID
				hasParent = true
			}
		} else {
			id = w.Usage.ID
		}
		if state.hasEntryOrUsageID(id) || transactionIDs[id] {
			return fmt.Errorf("session: duplicate entry or usage id: %s", id)
		}
		if isEntry && hasParent && !state.hasEntryID(parentID) && !transactionEntryIDs[parentID] {
			return fmt.Errorf("session: missing parent entry: %s", parentID)
		}
		transactionIDs[id] = true
		if isEntry {
			transactionEntryIDs[id] = true
		}
	}
	return nil
}
