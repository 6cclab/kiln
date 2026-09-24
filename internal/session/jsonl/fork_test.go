package jsonl

import (
	"crypto/sha256"
	"os"
	"testing"

	"github.com/andrepato/harness/internal/session"
)

// TestForkTreeLeavesSourceUnchanged forks the 1090-line fixture with
// tree scope and asserts: the source file's bytes are unchanged, the
// destination has the same entry ids and seqs as the source, and every
// excluded namespace (pi.op.*, pi.pending.*, pi.result, usage rows) is
// absent from the destination.
func TestForkTreeLeavesSourceUnchanged(t *testing.T) {
	before := sha256File(t, largeFixture)

	src, err := Open(largeFixture, nil)
	if err != nil {
		t.Fatal(err)
	}
	header := src.Header()
	nextSeq := src.NextSeq()
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}

	destPath := t.TempDir() + "/fork.jsonl"
	fixedNow := func() int64 { return 2_000_000_000_000 }
	if err := Fork(largeFixture, header, nextSeq, destPath, ForkOptions{
		Scope: session.ForkScopeTree,
		ID:    "forked-id",
	}, fixedNow); err != nil {
		t.Fatal(err)
	}

	after := sha256File(t, largeFixture)
	if before != after {
		t.Fatal("fork modified the source file")
	}

	dest, err := Open(destPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dest.Close()

	sourceEntries := scanAllEntries(t, largeFixture)
	destEntries := entriesByID(dest.ScanEntries(session.EntryScan{}))
	if len(destEntries) != len(sourceEntries) {
		t.Fatalf("destination has %d entries, source has %d", len(destEntries), len(sourceEntries))
	}
	for id, srcEntry := range sourceEntries {
		got, ok := destEntries[id]
		if !ok {
			t.Fatalf("destination missing entry %s", id)
		}
		if got.Seq != srcEntry.Seq {
			t.Fatalf("entry %s seq = %d, want %d", id, got.Seq, srcEntry.Seq)
		}
	}

	for _, ns := range []string{
		session.NamespaceOpMeta, session.NamespaceOpState, session.NamespaceOpToolArgs,
		session.NamespacePendingEntry, session.NamespacePendingToolOutput, session.NamespaceResult,
	} {
		if vals := dest.ScanValues(ns, ""); len(vals) != 0 {
			t.Errorf("destination has %d values under excluded namespace %s", len(vals), ns)
		}
	}
	if usage := dest.ScanUsage(session.UsageScan{}); len(usage) != 0 {
		t.Errorf("destination has %d usage rows, want 0", len(usage))
	}

	if dest.Header().ParentSessionID != header.ID {
		t.Errorf("dest parentSessionId = %q, want %q", dest.Header().ParentSessionID, header.ID)
	}
}

func sha256File(t *testing.T, path string) [32]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(data)
}

func scanAllEntries(t *testing.T, path string) map[string]session.Entry {
	t.Helper()
	st, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	return entriesByID(st.ScanEntries(session.EntryScan{}))
}

func entriesByID(entries []session.Entry) map[string]session.Entry {
	out := make(map[string]session.Entry, len(entries))
	for _, e := range entries {
		out[e.ID] = e
	}
	return out
}
