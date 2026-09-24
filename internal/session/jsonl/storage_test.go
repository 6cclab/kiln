package jsonl

import (
	"testing"

	"github.com/andrepato/harness/internal/session"
)

const largeFixture = "../../../testdata/sessions/2026-09-23T11-37-47-498Z_01a0ce0e-bcea-7701-a97e-cc374e8c56d1.jsonl"

// threeSeedWrites returns three arbitrary value writes (a lane's tip,
// config and state), used only to give storage-layer tests a nonzero seq
// to start from. It is not a copy of any production write sequence — the
// harness package (internal/harness) is what actually writes a lane's
// initial values, on that lane's first use.
func threeSeedWrites(lane string) []session.Write {
	tip, _ := session.SetValue(session.BranchTip(lane), (*string)(nil))
	cfg, _ := session.SetValue(session.LaneConfig(lane), session.LaneConfiguration{
		Model:           session.ModelRef{Provider: "ollama", ModelID: "qwen3.8:latest"},
		ThinkingLevel:   "off",
		ActiveToolNames: []string{"bash"},
	})
	st, _ := session.SetValue(session.LaneStateValue(lane), session.LaneState{Inbox: []session.InboxItem{}})
	return []session.Write{tip, cfg, st}
}

// TestOpenLargeFixture opens the 1090-line real fixture and asserts header
// identity, lane configuration, the branch tip, and that ScanBranch from
// that tip reaches every entry with every parentId resolved.
func TestOpenLargeFixture(t *testing.T) {
	st, err := Open(largeFixture, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	h := st.Header()
	if h.ID != "01a0ce0e-bcea-7701-a97e-cc374e8c56d1" {
		t.Fatalf("header id = %q", h.ID)
	}
	if h.Cwd != "/Users/tester/projects/harness" {
		t.Fatalf("header cwd = %q", h.Cwd)
	}

	cfgRaw, _, ok := st.GetValue(session.NamespaceLaneConfig, "main")
	if !ok {
		t.Fatal("missing pi.lane.config/main")
	}
	cfg, err := session.GetTypedValue[session.LaneConfiguration](cfgRaw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.Provider != "ollama" || cfg.Model.ModelID != "qwen3.8:latest" {
		t.Fatalf("lane config model = %+v", cfg.Model)
	}

	tipRaw, _, ok := st.GetValue(session.NamespaceBranchTip, "main")
	if !ok {
		t.Fatal("missing pi.branch.tip/main")
	}
	tip, err := session.GetTypedValue[*string](tipRaw)
	if err != nil {
		t.Fatal(err)
	}
	if tip == nil {
		t.Fatal("branch tip is nil")
	}
	const wantTip = "01a0ce0f-f0b5-7701-a97e-cc86314ac55a"
	if *tip != wantTip {
		t.Fatalf("branch tip = %q, want %q", *tip, wantTip)
	}

	entries, err := st.ScanBranch(session.BranchScan{Start: *tip, Order: "oldestFirst"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 10 {
		t.Fatalf("ScanBranch length = %d, want 10", len(entries))
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.ParentID != nil {
			if !seen[*e.ParentID] {
				t.Fatalf("entry %s parentId %s not seen before it in oldest-first scan", e.ID, *e.ParentID)
			}
		}
		seen[e.ID] = true
	}
}

// TestCommitAndReopen commits a user-message entry plus a branch-tip update
// to a fresh session, closes it, reopens it, and asserts the write is
// present and seq continues from where it left off.
func TestCommitAndReopen(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/session.jsonl"
	header := session.Header{
		V: session.FormatVersion, Kind: "header", ID: "test-id",
		StorageVersion: session.StorageVersion, CreatedAt: 1000, Cwd: "/tmp/proj",
	}
	st, err := Create(path, header, threeSeedWrites("main"), nil)
	if err != nil {
		t.Fatal(err)
	}

	entryID := "entry-1"
	userMsg := session.EntryWrite{Entry: session.Entry{
		ID:   entryID,
		Type: session.EntryMessage,
	}}
	tipWrite, err := session.SetValue(session.BranchTip("main"), &entryID)
	if err != nil {
		t.Fatal(err)
	}
	res, err := st.Commit([]session.Write{userMsg, tipWrite})
	if err != nil {
		t.Fatal(err)
	}
	if res.FirstSeq != 4 { // 3 initial lane writes already used seq 1-3
		t.Fatalf("firstSeq = %d, want 4", res.FirstSeq)
	}
	nextSeqBeforeClose := st.NextSeq()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.NextSeq() != nextSeqBeforeClose {
		t.Fatalf("nextSeq after reopen = %d, want %d", reopened.NextSeq(), nextSeqBeforeClose)
	}
	got, ok := reopened.GetEntry(entryID)
	if !ok {
		t.Fatal("entry missing after reopen")
	}
	if got.Type != session.EntryMessage {
		t.Fatalf("entry type = %q", got.Type)
	}
	tipRaw, _, ok := reopened.GetValue(session.NamespaceBranchTip, "main")
	if !ok {
		t.Fatal("missing branch tip after reopen")
	}
	tip, err := session.GetTypedValue[*string](tipRaw)
	if err != nil {
		t.Fatal(err)
	}
	if tip == nil || *tip != entryID {
		t.Fatalf("branch tip after reopen = %v, want %s", tip, entryID)
	}

	// Commit again to prove seq continues monotonically across the reopen.
	res2, err := reopened.Commit([]session.Write{session.EntryWrite{Entry: session.Entry{
		ID: "entry-2", ParentID: &entryID, Type: session.EntryMessage,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if res2.FirstSeq <= res.Seqs[len(res.Seqs)-1] {
		t.Fatalf("seq did not continue: first commit last seq %d, second commit first seq %d", res.Seqs[len(res.Seqs)-1], res2.FirstSeq)
	}
}
