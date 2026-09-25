package session

import (
	"encoding/json"
	"testing"

	"github.com/andrepato/harness/internal/msg"
)

func TestNewStateZeroValue(t *testing.T) {
	s := NewState()
	if s.NextSeq() != 1 {
		t.Fatalf("NewState().NextSeq() = %d, want 1", s.NextSeq())
	}
	if got := s.GetStats(); got.MessageCount != 0 || got.Usage != (msg.Usage{}) {
		t.Fatalf("NewState().GetStats() = %+v, want zero", got)
	}
	if entries := s.GetEntries([]string{"anything"}); len(entries) != 0 {
		t.Fatalf("NewState().GetEntries() = %v, want empty", entries)
	}
	if _, ok := s.GetEntry("anything"); ok {
		t.Fatalf("NewState().GetEntry() ok=true, want false")
	}
	if vals := s.ScanValues("ns", ""); vals != nil {
		t.Fatalf("NewState().ScanValues() = %v, want nil", vals)
	}
	if entries := s.ScanEntries(EntryScan{}); entries != nil {
		t.Fatalf("NewState().ScanEntries() = %v, want nil", entries)
	}
	if rows := s.ScanUsage(UsageScan{}); len(rows) != 0 {
		t.Fatalf("NewState().ScanUsage() = %v, want empty", rows)
	}
}

// --- ApplyValidated ------------------------------------------------------

func TestApplyValidatedEntryWrite(t *testing.T) {
	s := NewState()
	commitAll(t, s, []Write{
		EntryWrite{Entry: Entry{ID: "e1", Type: EntryMessage}},
	}, 100)

	e, ok := s.GetEntry("e1")
	if !ok {
		t.Fatalf("GetEntry(e1) ok=false, want true")
	}
	if e.Seq != 1 || e.Timestamp != 100 {
		t.Fatalf("entry = %+v, want seq=1 timestamp=100", e)
	}
	stats := s.GetStats()
	if stats.MessageCount != 1 {
		t.Fatalf("MessageCount = %d, want 1", stats.MessageCount)
	}
	if s.NextSeq() != 2 {
		t.Fatalf("NextSeq() = %d, want 2", s.NextSeq())
	}
}

func TestApplyValidatedNonMessageEntryDoesNotCountMessages(t *testing.T) {
	s := NewState()
	commitAll(t, s, []Write{
		EntryWrite{Entry: Entry{ID: "c1", Type: EntryCompaction, TokensBefore: 10}},
	}, 0)
	if stats := s.GetStats(); stats.MessageCount != 0 {
		t.Fatalf("MessageCount = %d, want 0 for non-message entry", stats.MessageCount)
	}
}

func TestApplyValidatedUsageWrite(t *testing.T) {
	s := NewState()
	commitAll(t, s, []Write{
		UsageWrite{Row: UsageRow{ID: "u1", Usage: msg.Usage{Input: 5, Output: 7}}},
	}, 0)
	stats := s.GetStats()
	if stats.Usage.Input != 5 || stats.Usage.Output != 7 {
		t.Fatalf("Usage = %+v, want input=5 output=7", stats.Usage)
	}
	rows := s.ScanUsage(UsageScan{})
	if len(rows) != 1 || rows[0].ID != "u1" {
		t.Fatalf("ScanUsage() = %+v, want [u1]", rows)
	}
}

func TestApplyValidatedValueSetAndDelete(t *testing.T) {
	s := NewState()
	setW, err := SetValue(SessionName(), "hello")
	if err != nil {
		t.Fatalf("SetValue: %v", err)
	}
	commitAll(t, s, []Write{setW}, 0)

	v, ok := s.GetValue(NamespaceSessionName, "")
	if !ok {
		t.Fatalf("GetValue after set: ok=false")
	}
	if string(v.Value) != `"hello"` {
		t.Fatalf("GetValue after set = %s, want %q", v.Value, `"hello"`)
	}

	commitAll(t, s, []Write{DeleteValue(SessionName())}, 0)
	if _, ok := s.GetValue(NamespaceSessionName, ""); ok {
		t.Fatalf("GetValue after delete: ok=true, want false")
	}
}

func TestApplyValidatedListAppendAndDelete(t *testing.T) {
	s := NewState()
	addr := PendingAssistantFrames("op-1", "e1")
	w1, err := AppendListWrite(addr, json.RawMessage(`{"n":1}`))
	if err != nil {
		t.Fatalf("AppendListWrite: %v", err)
	}
	w2, err := AppendListWrite(addr, json.RawMessage(`{"n":2}`))
	if err != nil {
		t.Fatalf("AppendListWrite: %v", err)
	}
	commitAll(t, s, []Write{w1, w2}, 0)

	els := s.ReadList(NamespacePendingAssistantFrame, "op-1:e1")
	if len(els) != 2 {
		t.Fatalf("ReadList() len = %d, want 2", len(els))
	}
	if string(els[0].Value) != `{"n":1}` || string(els[1].Value) != `{"n":2}` {
		t.Fatalf("ReadList() = %+v, want ordered append", els)
	}

	commitAll(t, s, []Write{DeleteListWrite(addr)}, 0)
	if els := s.ReadList(NamespacePendingAssistantFrame, "op-1:e1"); els != nil {
		t.Fatalf("ReadList() after delete = %v, want nil", els)
	}
}

// --- GetEntries / GetEntry ------------------------------------------------

func TestGetEntriesFoundAndMissing(t *testing.T) {
	s := NewState()
	commitAll(t, s, []Write{
		EntryWrite{Entry: Entry{ID: "e1", Type: EntryMessage}},
		EntryWrite{Entry: Entry{ID: "e2", ParentID: strPtr("e1"), Type: EntryMessage}},
	}, 0)

	got := s.GetEntries([]string{"e1", "e2", "missing"})
	if len(got) != 2 {
		t.Fatalf("GetEntries() len = %d, want 2", len(got))
	}
	if _, ok := got["e1"]; !ok {
		t.Fatalf("GetEntries() missing e1")
	}
	if _, ok := got["missing"]; ok {
		t.Fatalf("GetEntries() should not include missing id")
	}
}

// --- ScanEntries -----------------------------------------------------------

func seedEntries(t *testing.T, s *State) {
	t.Helper()
	commitAll(t, s, []Write{
		EntryWrite{Entry: Entry{ID: "e1", Type: EntryMessage}},
		EntryWrite{Entry: Entry{ID: "e2", ParentID: strPtr("e1"), Type: EntryCompaction}},
		EntryWrite{Entry: Entry{ID: "e3", ParentID: strPtr("e2"), Type: EntryCustom, CustomType: "note"}},
		EntryWrite{Entry: Entry{ID: "e4", ParentID: strPtr("e3"), Type: EntryMessage}},
		EntryWrite{Entry: Entry{ID: "e5", ParentID: strPtr("e4"), Type: EntryCustom, CustomType: "other"}},
	}, 0)
}

func TestScanEntriesTypeFilter(t *testing.T) {
	s := NewState()
	seedEntries(t, s)
	got := s.ScanEntries(EntryScan{Type: EntryMessage})
	if len(got) != 2 {
		t.Fatalf("ScanEntries(Type=message) len = %d, want 2: %+v", len(got), got)
	}
	for _, e := range got {
		if e.Type != EntryMessage {
			t.Fatalf("ScanEntries(Type=message) returned %+v", e)
		}
	}
}

func TestScanEntriesCustomTypeFilter(t *testing.T) {
	s := NewState()
	seedEntries(t, s)
	got := s.ScanEntries(EntryScan{Type: EntryCustom, CustomType: "note"})
	if len(got) != 1 || got[0].ID != "e3" {
		t.Fatalf("ScanEntries(CustomType=note) = %+v, want [e3]", got)
	}
}

func TestScanEntriesFromToSeqRange(t *testing.T) {
	s := NewState()
	seedEntries(t, s)
	got := s.ScanEntries(EntryScan{FromSeq: 2, ToSeq: 4})
	if len(got) != 3 {
		t.Fatalf("ScanEntries(FromSeq=2,ToSeq=4) len = %d, want 3: %+v", len(got), got)
	}
	for _, e := range got {
		if e.Seq < 2 || e.Seq > 4 {
			t.Fatalf("ScanEntries(FromSeq=2,ToSeq=4) returned out-of-range seq %d", e.Seq)
		}
	}
}

func TestScanEntriesOrderAscDesc(t *testing.T) {
	s := NewState()
	seedEntries(t, s)

	asc := s.ScanEntries(EntryScan{Order: "asc"})
	for i := 1; i < len(asc); i++ {
		if asc[i].Seq < asc[i-1].Seq {
			t.Fatalf("ScanEntries(asc) not ascending: %+v", asc)
		}
	}

	desc := s.ScanEntries(EntryScan{Order: "desc"})
	for i := 1; i < len(desc); i++ {
		if desc[i].Seq > desc[i-1].Seq {
			t.Fatalf("ScanEntries(desc) not descending: %+v", desc)
		}
	}
	if asc[0].ID != desc[len(desc)-1].ID {
		t.Fatalf("asc/desc endpoints mismatch: asc[0]=%s desc[last]=%s", asc[0].ID, desc[len(desc)-1].ID)
	}
}

func TestScanEntriesLimit(t *testing.T) {
	s := NewState()
	seedEntries(t, s)
	got := s.ScanEntries(EntryScan{Limit: 2})
	if len(got) != 2 {
		t.Fatalf("ScanEntries(Limit=2) len = %d, want 2", len(got))
	}
	if got[0].ID != "e1" || got[1].ID != "e2" {
		t.Fatalf("ScanEntries(Limit=2) = %+v, want first two in seq order", got)
	}
}

func TestScanEntriesEmptyState(t *testing.T) {
	s := NewState()
	got := s.ScanEntries(EntryScan{})
	if got != nil {
		t.Fatalf("ScanEntries() on empty state = %v, want nil", got)
	}
}

// --- ScanUsage ---------------------------------------------------------

func seedUsage(t *testing.T, s *State) {
	t.Helper()
	commitAll(t, s, []Write{
		UsageWrite{Row: UsageRow{ID: "u1", Usage: msg.Usage{Input: 1}}},
		UsageWrite{Row: UsageRow{ID: "u2", Usage: msg.Usage{Input: 2}}},
		UsageWrite{Row: UsageRow{ID: "u3", Usage: msg.Usage{Input: 3}, Adjustment: true}},
		UsageWrite{Row: UsageRow{ID: "u4", Usage: msg.Usage{Input: 4}}},
	}, 0)
}

func TestScanUsageFromToSeqRange(t *testing.T) {
	s := NewState()
	seedUsage(t, s)
	got := s.ScanUsage(UsageScan{FromSeq: 2, ToSeq: 3})
	if len(got) != 2 {
		t.Fatalf("ScanUsage(FromSeq=2,ToSeq=3) len = %d, want 2: %+v", len(got), got)
	}
}

func TestScanUsageOrderAscDesc(t *testing.T) {
	s := NewState()
	seedUsage(t, s)

	asc := s.ScanUsage(UsageScan{Order: "asc"})
	for i := 1; i < len(asc); i++ {
		if asc[i].Seq < asc[i-1].Seq {
			t.Fatalf("ScanUsage(asc) not ascending: %+v", asc)
		}
	}
	desc := s.ScanUsage(UsageScan{Order: "desc"})
	for i := 1; i < len(desc); i++ {
		if desc[i].Seq > desc[i-1].Seq {
			t.Fatalf("ScanUsage(desc) not descending: %+v", desc)
		}
	}
}

func TestScanUsageLimit(t *testing.T) {
	s := NewState()
	seedUsage(t, s)
	got := s.ScanUsage(UsageScan{Order: "asc", Limit: 2})
	if len(got) != 2 {
		t.Fatalf("ScanUsage(Limit=2) len = %d, want 2", len(got))
	}
	if got[0].ID != "u1" || got[1].ID != "u2" {
		t.Fatalf("ScanUsage(Limit=2) = %+v, want [u1,u2]", got)
	}
}

func TestScanUsageAdjustmentRow(t *testing.T) {
	s := NewState()
	seedUsage(t, s)
	got := s.ScanUsage(UsageScan{})
	var found *UsageRow
	for i := range got {
		if got[i].ID == "u3" {
			found = &got[i]
		}
	}
	if found == nil {
		t.Fatalf("ScanUsage() missing u3")
	}
	if !found.Adjustment {
		t.Fatalf("u3.Adjustment = false, want true")
	}
}

// --- ScanBranch ----------------------------------------------------------

func TestScanBranchStraightChain(t *testing.T) {
	s := NewState()
	seedEntries(t, s)
	got, err := s.ScanBranch(BranchScan{Start: "e5", Order: "oldestFirst"})
	if err != nil {
		t.Fatalf("ScanBranch: %v", err)
	}
	wantIDs := []string{"e1", "e2", "e3", "e4", "e5"}
	if len(got) != len(wantIDs) {
		t.Fatalf("ScanBranch() len = %d, want %d: %+v", len(got), len(wantIDs), got)
	}
	for i, id := range wantIDs {
		if got[i].ID != id {
			t.Fatalf("ScanBranch()[%d] = %s, want %s", i, got[i].ID, id)
		}
	}
}

func TestScanBranchNewestFirstOrder(t *testing.T) {
	s := NewState()
	seedEntries(t, s)
	got, err := s.ScanBranch(BranchScan{Start: "e5", Order: "newestFirst"})
	if err != nil {
		t.Fatalf("ScanBranch: %v", err)
	}
	wantIDs := []string{"e5", "e4", "e3", "e2", "e1"}
	for i, id := range wantIDs {
		if got[i].ID != id {
			t.Fatalf("ScanBranch(newestFirst)[%d] = %s, want %s", i, got[i].ID, id)
		}
	}
}

func TestScanBranchFork(t *testing.T) {
	s := NewState()
	// root -> a -> b, and a fork: root -> a -> c (two tips sharing "a").
	commitAll(t, s, []Write{
		EntryWrite{Entry: Entry{ID: "root", Type: EntryMessage}},
		EntryWrite{Entry: Entry{ID: "a", ParentID: strPtr("root"), Type: EntryMessage}},
		EntryWrite{Entry: Entry{ID: "b", ParentID: strPtr("a"), Type: EntryMessage}},
		EntryWrite{Entry: Entry{ID: "c", ParentID: strPtr("a"), Type: EntryMessage}},
	}, 0)

	gotB, err := s.ScanBranch(BranchScan{Start: "b", Order: "oldestFirst"})
	if err != nil {
		t.Fatalf("ScanBranch(b): %v", err)
	}
	wantB := []string{"root", "a", "b"}
	for i, id := range wantB {
		if gotB[i].ID != id {
			t.Fatalf("ScanBranch(b)[%d] = %s, want %s", i, gotB[i].ID, id)
		}
	}

	gotC, err := s.ScanBranch(BranchScan{Start: "c", Order: "oldestFirst"})
	if err != nil {
		t.Fatalf("ScanBranch(c): %v", err)
	}
	wantC := []string{"root", "a", "c"}
	for i, id := range wantC {
		if gotC[i].ID != id {
			t.Fatalf("ScanBranch(c)[%d] = %s, want %s", i, gotC[i].ID, id)
		}
	}
}

func TestScanBranchStopAtID(t *testing.T) {
	s := NewState()
	seedEntries(t, s)
	got, err := s.ScanBranch(BranchScan{Start: "e5", Order: "oldestFirst", StopAtID: "e3"})
	if err != nil {
		t.Fatalf("ScanBranch: %v", err)
	}
	// The path tip->root is reversed to oldestFirst (e1..e5) before the
	// stop condition is applied, so the walk proceeds e1, e2, e3 and
	// halts as soon as it reaches e3.
	want := []string{"e1", "e2", "e3"}
	if len(got) != len(want) {
		t.Fatalf("ScanBranch(StopAtID=e3) = %+v, want %v", got, want)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("ScanBranch(StopAtID=e3)[%d] = %s, want %s", i, got[i].ID, id)
		}
	}
}

func TestScanBranchStopAtType(t *testing.T) {
	s := NewState()
	seedEntries(t, s)
	got, err := s.ScanBranch(BranchScan{Start: "e5", Order: "oldestFirst", StopAtType: EntryCompaction})
	if err != nil {
		t.Fatalf("ScanBranch: %v", err)
	}
	// Walking oldestFirst (e1..e5), the stop condition fires as soon as
	// it reaches the first EntryCompaction entry (e2).
	want := []string{"e1", "e2"}
	if len(got) != len(want) {
		t.Fatalf("ScanBranch(StopAtType=compaction) = %+v, want %v", got, want)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("ScanBranch(StopAtType=compaction)[%d] = %s, want %s", i, got[i].ID, id)
		}
	}
}

func TestScanBranchCursorPagination(t *testing.T) {
	s := NewState()
	seedEntries(t, s)

	page1, err := s.ScanBranch(BranchScan{Start: "e5", Order: "oldestFirst", Limit: 2})
	if err != nil {
		t.Fatalf("ScanBranch page1: %v", err)
	}
	if len(page1) != 2 || page1[0].ID != "e1" || page1[1].ID != "e2" {
		t.Fatalf("page1 = %+v, want [e1,e2]", page1)
	}

	page2, err := s.ScanBranch(BranchScan{
		Start: "e5", Order: "oldestFirst", Limit: 2,
		Cursor: &EntryCursor{Seq: page1[len(page1)-1].Seq},
	})
	if err != nil {
		t.Fatalf("ScanBranch page2: %v", err)
	}
	if len(page2) != 2 || page2[0].ID != "e3" || page2[1].ID != "e4" {
		t.Fatalf("page2 = %+v, want [e3,e4]", page2)
	}

	page3, err := s.ScanBranch(BranchScan{
		Start: "e5", Order: "oldestFirst", Limit: 2,
		Cursor: &EntryCursor{Seq: page2[len(page2)-1].Seq},
	})
	if err != nil {
		t.Fatalf("ScanBranch page3: %v", err)
	}
	if len(page3) != 1 || page3[0].ID != "e5" {
		t.Fatalf("page3 = %+v, want [e5]", page3)
	}
}

func TestScanBranchCursorNewestFirst(t *testing.T) {
	s := NewState()
	seedEntries(t, s)
	page1, err := s.ScanBranch(BranchScan{Start: "e5", Order: "newestFirst", Limit: 2})
	if err != nil {
		t.Fatalf("ScanBranch page1: %v", err)
	}
	if len(page1) != 2 || page1[0].ID != "e5" || page1[1].ID != "e4" {
		t.Fatalf("page1 = %+v, want [e5,e4]", page1)
	}
	page2, err := s.ScanBranch(BranchScan{
		Start: "e5", Order: "newestFirst", Limit: 2,
		Cursor: &EntryCursor{Seq: page1[len(page1)-1].Seq},
	})
	if err != nil {
		t.Fatalf("ScanBranch page2: %v", err)
	}
	if len(page2) != 2 || page2[0].ID != "e3" || page2[1].ID != "e2" {
		t.Fatalf("page2 = %+v, want [e3,e2]", page2)
	}
}

func TestScanBranchUnknownStartID(t *testing.T) {
	s := NewState()
	seedEntries(t, s)
	_, err := s.ScanBranch(BranchScan{Start: "does-not-exist"})
	if err == nil {
		t.Fatalf("ScanBranch(unknown start): got nil error, want error")
	}
}

func TestScanBranchCorruptParent(t *testing.T) {
	s := NewState()
	// Build an entry whose parent id was never committed, by writing
	// directly into state internals via ApplyValidated bypassing
	// ValidateCommitted (which would normally reject this).
	corrupt := Entry{ID: "orphan", ParentID: strPtr("ghost-parent"), Seq: 1, Type: EntryMessage}
	s.ApplyValidated([]CommittedWrite{{Kind: "entry", Entry: &corrupt}})

	_, err := s.ScanBranch(BranchScan{Start: "orphan"})
	if err == nil {
		t.Fatalf("ScanBranch(corrupt parent): got nil error, want error")
	}
}

// --- ScanValues ------------------------------------------------------------

func TestScanValuesPrefixFilteringAndOrder(t *testing.T) {
	s := NewState()
	commitAll(t, s, []Write{
		SetValueRaw("ns", "b-key", json.RawMessage(`1`)),
		SetValueRaw("ns", "a-key", json.RawMessage(`2`)),
		SetValueRaw("ns", "c-other", json.RawMessage(`3`)),
		SetValueRaw("other-ns", "a-key", json.RawMessage(`4`)),
	}, 0)

	got := s.ScanValues("ns", "")
	if len(got) != 3 {
		t.Fatalf("ScanValues(ns,\"\") len = %d, want 3: %+v", len(got), got)
	}
	wantOrder := []string{"a-key", "b-key", "c-other"}
	for i, k := range wantOrder {
		if got[i].Key != k {
			t.Fatalf("ScanValues(ns,\"\")[%d].Key = %s, want %s", i, got[i].Key, k)
		}
	}

	prefixed := s.ScanValues("ns", "a-")
	if len(prefixed) != 1 || prefixed[0].Key != "a-key" {
		t.Fatalf("ScanValues(ns,\"a-\") = %+v, want [a-key]", prefixed)
	}
}

func TestScanValuesEmptyResult(t *testing.T) {
	s := NewState()
	commitAll(t, s, []Write{SetValueRaw("ns", "k", json.RawMessage(`1`))}, 0)
	got := s.ScanValues("ns", "no-match-")
	if got != nil {
		t.Fatalf("ScanValues(no matches) = %v, want nil", got)
	}
	got2 := s.ScanValues("nonexistent-ns", "")
	if got2 != nil {
		t.Fatalf("ScanValues(nonexistent ns) = %v, want nil", got2)
	}
}

// --- GetStats --------------------------------------------------------------

func TestGetStatsAggregatesMessagesAndUsageIncludingAdjustment(t *testing.T) {
	s := NewState()
	commitAll(t, s, []Write{
		EntryWrite{Entry: Entry{ID: "e1", Type: EntryMessage}},
		EntryWrite{Entry: Entry{ID: "e2", ParentID: strPtr("e1"), Type: EntryMessage}},
		EntryWrite{Entry: Entry{ID: "e3", ParentID: strPtr("e2"), Type: EntryCompaction}},
		UsageWrite{Row: UsageRow{ID: "u1", Usage: msg.Usage{Input: 10, Output: 5}}},
		UsageWrite{Row: UsageRow{ID: "u2", Usage: msg.Usage{Input: 3}, Adjustment: true}},
	}, 0)

	stats := s.GetStats()
	if stats.MessageCount != 2 {
		t.Fatalf("MessageCount = %d, want 2", stats.MessageCount)
	}
	if stats.Usage.Input != 13 || stats.Usage.Output != 5 {
		t.Fatalf("Usage = %+v, want input=13 output=5 (adjustment included)", stats.Usage)
	}
}

// --- PrepareCommit / ValidateCommitted / ApplyValidated integration --------

func TestStatePrepareCommitIntegration(t *testing.T) {
	s := NewState()
	committed, result, err := s.PrepareCommit([]Write{
		EntryWrite{Entry: Entry{ID: "e1", Type: EntryMessage}},
	}, 500)
	if err != nil {
		t.Fatalf("PrepareCommit: %v", err)
	}
	if result.FirstSeq != 1 || result.Timestamp != 500 {
		t.Fatalf("result = %+v, want FirstSeq=1 Timestamp=500", result)
	}
	// Not yet applied.
	if _, ok := s.GetEntry("e1"); ok {
		t.Fatalf("entry visible before ApplyValidated")
	}
	s.ApplyValidated(committed)
	if _, ok := s.GetEntry("e1"); !ok {
		t.Fatalf("entry not visible after ApplyValidated")
	}
}

func TestStatePrepareCommitRejectsDuplicateID(t *testing.T) {
	s := NewState()
	commitAll(t, s, []Write{EntryWrite{Entry: Entry{ID: "e1", Type: EntryMessage}}}, 0)

	_, _, err := s.PrepareCommit([]Write{EntryWrite{Entry: Entry{ID: "e1", Type: EntryMessage}}}, 0)
	if err == nil {
		t.Fatalf("PrepareCommit(duplicate id): got nil error, want error")
	}
}

func TestStatePrepareCommitRejectsMissingParent(t *testing.T) {
	s := NewState()
	_, _, err := s.PrepareCommit([]Write{
		EntryWrite{Entry: Entry{ID: "e1", ParentID: strPtr("nonexistent"), Type: EntryMessage}},
	}, 0)
	if err == nil {
		t.Fatalf("PrepareCommit(missing parent): got nil error, want error")
	}
}

// --- AdvanceNextSeq ----------------------------------------------------

func TestAdvanceNextSeqRaisesOnlyWhenHigher(t *testing.T) {
	s := NewState()
	if err := s.AdvanceNextSeq(5); err != nil {
		t.Fatalf("AdvanceNextSeq(5): %v", err)
	}
	if s.NextSeq() != 5 {
		t.Fatalf("NextSeq() = %d, want 5", s.NextSeq())
	}
	if err := s.AdvanceNextSeq(3); err != nil {
		t.Fatalf("AdvanceNextSeq(3): %v", err)
	}
	if s.NextSeq() != 5 {
		t.Fatalf("NextSeq() = %d after lower advance, want unchanged 5", s.NextSeq())
	}
}

func TestAdvanceNextSeqErrorsBelowOne(t *testing.T) {
	s := NewState()
	if err := s.AdvanceNextSeq(0); err == nil {
		t.Fatalf("AdvanceNextSeq(0): got nil error, want error")
	}
	if err := s.AdvanceNextSeq(-1); err == nil {
		t.Fatalf("AdvanceNextSeq(-1): got nil error, want error")
	}
}
