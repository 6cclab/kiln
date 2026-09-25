package session

import "testing"

// commitAll runs writes through PrepareCommit, ValidateCommitted and
// ApplyValidated against s, the same sequence a real Storage.Commit uses,
// and fails the test on any error. It returns the committed writes so
// callers can inspect assigned seqs/timestamps.
func commitAll(t *testing.T, s *State, writes []Write, timestamp int64) []CommittedWrite {
	t.Helper()
	committed, _, err := s.PrepareCommit(writes, timestamp)
	if err != nil {
		t.Fatalf("PrepareCommit: %v", err)
	}
	s.ApplyValidated(committed)
	return committed
}

func strPtr(s string) *string { return &s }
