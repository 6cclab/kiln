package jsonl

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

const legacyV3Fixture = "../../../testdata/sessions/legacy-v3-fixture.jsonl"

// copyFixture copies the committed legacy v3 fixture into a fresh temp file
// so tests can let Open (or UpgradeLegacyV3) rewrite it without touching
// testdata.
func copyFixture(t *testing.T, src string) string {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "legacy.jsonl")
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dst
}

// TestOpenLegacyV3Fixture upgrades testdata/sessions/legacy-v3-fixture.jsonl
// (see its README entry for what it encodes and why it's synthetic) and
// checks the resulting v4 session against what legacy-v3.js's mapping
// implies for it, entry by entry. Retained entries come out of ScanEntries
// in seq order, which is exactly file order (e1, e2, e3, e4, b1, b2, b2c,
// cu1, comp1, bs1) — id order and value derivation, not literal id strings,
// are what's asserted, since ids are freshly minted (legacy-v3.js:239,
// uuidv7(Date.parse(entry.timestamp))).
func TestOpenLegacyV3Fixture(t *testing.T) {
	path := copyFixture(t, legacyV3Fixture)

	st, err := Open(path, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	h := st.Header()
	if h.V != session.FormatVersion || h.Kind != "header" {
		t.Fatalf("header = %+v, not v4", h)
	}
	if h.ID != "legacy-fixture-1" {
		t.Fatalf("header id = %q", h.ID)
	}
	if h.Cwd != "/tmp/legacy-project" {
		t.Fatalf("header cwd = %q", h.Cwd)
	}
	if h.CreatedAt != 1704067200000 {
		t.Fatalf("header createdAt = %d, want 1704067200000", h.CreatedAt)
	}

	entries := st.ScanEntries(session.EntryScan{Order: "asc"})
	// e1 e2 e3 e4 b1 b2 b2c cu1 comp1 bs1 (mc1/tlc1/atc1/lbl1/si1 fold into values).
	if len(entries) != 10 {
		t.Fatalf("ScanEntries returned %d entries, want 10", len(entries))
	}
	for i, e := range entries {
		if e.Seq != int64(i+1) {
			t.Fatalf("entries[%d].Seq = %d, want %d", i, e.Seq, i+1)
		}
	}
	e1, e2, e3, e4, b1, b2, b2c, cu1, comp1, bs1 := entries[0], entries[1], entries[2], entries[3], entries[4], entries[5], entries[6], entries[7], entries[8], entries[9]

	// Topology: e1 <- e2 <- e3 <- e4 (through the model_change/
	// thinking_level_change/active_tools_change pass-through chain) <- {b1,
	// b2} <- b2c <- cu1 <- comp1 <- bs1.
	mustNilParent(t, "e1", e1)
	mustParent(t, "e2", e2, e1.ID)
	mustParent(t, "e3", e3, e2.ID)
	mustParent(t, "e4", e4, e3.ID) // proves mc1/tlc1/atc1 pass their mapped id straight through
	mustParent(t, "b1", b1, e4.ID)
	mustParent(t, "b2", b2, e4.ID) // b1 and b2 both parent on e4: the branch point
	mustParent(t, "b2c", b2c, b2.ID)
	mustParent(t, "cu1", cu1, b2c.ID)
	mustParent(t, "comp1", comp1, cu1.ID)
	mustParent(t, "bs1", bs1, comp1.ID)

	// Types.
	for _, e := range []session.Entry{e1, e2, e3, e4, b1, b2, b2c} {
		if e.Type != session.EntryMessage {
			t.Fatalf("entry %s type = %q, want message", e.ID, e.Type)
		}
	}
	if cu1.Type != session.EntryCustom || cu1.CustomType != "note" {
		t.Fatalf("cu1 = %+v, want custom/note", cu1)
	}
	if string(cu1.Data) != `{"text":"remember to add a test"}` {
		t.Fatalf("cu1 data = %s", cu1.Data)
	}
	if comp1.Type != session.EntryCompaction {
		t.Fatalf("comp1 type = %q, want compaction", comp1.Type)
	}
	if comp1.Summary != "compacted early turns" || comp1.TokensBefore != 500 {
		t.Fatalf("comp1 = %+v", comp1)
	}
	if bs1.Type != session.EntryBranchSummary || bs1.Summary != "branch summary text" {
		t.Fatalf("bs1 = %+v", bs1)
	}
	if bs1.FromID == nil || *bs1.FromID != b1.ID {
		t.Fatalf("bs1.FromID = %v, want b1's id %s (legacy fromId was \"b1\", not the \"root\" sentinel)", bs1.FromID, b1.ID)
	}

	// comp1's retainedTail walks physically from comp1.parentId (cu1, a
	// custom entry — excluded, legacy-v3.js:398) back through
	// firstKeptEntryId ("e4"), inclusive: b2c, b2, e4, reversed to oldest
	// first.
	if len(comp1.RetainedTail) != 3 {
		t.Fatalf("comp1.RetainedTail has %d messages, want 3: %+v", len(comp1.RetainedTail), comp1.RetainedTail)
	}
	wantTailTexts := []string{"Fixed it, want me to keep going?", "Yes, keep going.", "Done."}
	for i, m := range comp1.RetainedTail {
		got := textOfMessage(t, m)
		if got != wantTailTexts[i] {
			t.Fatalf("RetainedTail[%d] = %q, want %q", i, got, wantTailTexts[i])
		}
	}

	// Message payload fidelity: e2's assistant message kept its toolCall.
	am, ok := e2.Message.(msg.AssistantMessage)
	if !ok {
		t.Fatalf("e2.Message is %T, want AssistantMessage", e2.Message)
	}
	calls := msg.ToolCallsOf(am.Content)
	if len(calls) != 1 || calls[0].Name != "read" || calls[0].ID != "call-1" {
		t.Fatalf("e2 toolCalls = %+v", calls)
	}

	// Values derived after the entries.
	tipRaw, _, ok := st.GetValue(session.NamespaceBranchTip, "main")
	if !ok {
		t.Fatal("missing pi.branch.tip/main")
	}
	tip, err := session.GetTypedValue[*string](tipRaw)
	if err != nil {
		t.Fatal(err)
	}
	if tip == nil || *tip != bs1.ID {
		t.Fatalf("branch tip = %v, want bs1's id %s (bs1 is the file's last physical entry, reached through the lbl1/si1 pass-through)", tip, bs1.ID)
	}

	cfgRaw, _, ok := st.GetValue(session.NamespaceLaneConfig, "main")
	if !ok {
		t.Fatal("missing pi.lane.config/main")
	}
	cfg, err := session.GetTypedValue[session.LaneConfiguration](cfgRaw)
	if err != nil {
		t.Fatal(err)
	}
	// mc1 is the only model_change; the walk from bs1 back through
	// comp1/cu1/b2c/b2/e4/atc1/tlc1/mc1 finds it, tlc1 and atc1.
	if cfg.Model.Provider != "anthropic" || cfg.Model.ModelID != "claude-y" {
		t.Fatalf("laneConfig.Model = %+v", cfg.Model)
	}
	if cfg.ThinkingLevel != "medium" {
		t.Fatalf("laneConfig.ThinkingLevel = %q", cfg.ThinkingLevel)
	}
	if len(cfg.ActiveToolNames) != 2 || cfg.ActiveToolNames[0] != "bash" || cfg.ActiveToolNames[1] != "read" {
		t.Fatalf("laneConfig.ActiveToolNames = %v", cfg.ActiveToolNames)
	}

	stateRaw, _, ok := st.GetValue(session.NamespaceLaneState, "main")
	if !ok {
		t.Fatal("missing pi.lane.state/main")
	}
	state, err := session.GetTypedValue[session.LaneState](stateRaw)
	if err != nil {
		t.Fatal(err)
	}
	if state.CurrentOperationID != nil || state.LastOperationID != nil || len(state.Inbox) != 0 {
		t.Fatalf("laneState = %+v, want zero state", state)
	}

	labelRaw, _, ok := st.GetValue(session.NamespaceEntryLabel, e2.ID)
	if !ok {
		t.Fatal("missing pi.entry.label for e2")
	}
	label, err := session.GetTypedValue[string](labelRaw)
	if err != nil {
		t.Fatal(err)
	}
	if label != "important" {
		t.Fatalf("label on e2 = %q, want %q", label, "important")
	}

	nameRaw, _, ok := st.GetValue(session.NamespaceSessionName, "")
	if !ok {
		t.Fatal("missing pi.session.name")
	}
	name, err := session.GetTypedValue[string](nameRaw)
	if err != nil {
		t.Fatal(err)
	}
	if name != "Fixture Session" {
		t.Fatalf("session name = %q", name)
	}

	// importedUsage: every assistant/toolResult message's usage, plus
	// comp1's and bs1's usage, summed into one usage-adjustment row
	// (legacy-v3.js:251-263, 285-287). e1/b1/b2 (user messages) and cu1
	// (custom) contribute nothing.
	stats := st.GetStats()
	if stats.Usage.Input != 197 {
		t.Fatalf("stats.Usage.Input = %d, want 197 (100+10+50+30+5+2)", stats.Usage.Input)
	}
	if stats.Usage.Output != 45 {
		t.Fatalf("stats.Usage.Output = %d, want 45 (20+5+10+8+1+1)", stats.Usage.Output)
	}
	if stats.Usage.TotalTokens != 242 {
		t.Fatalf("stats.Usage.TotalTokens = %d, want 242 (120+15+60+38+6+3)", stats.Usage.TotalTokens)
	}
}

func mustNilParent(t *testing.T, name string, e session.Entry) {
	t.Helper()
	if e.ParentID != nil {
		t.Fatalf("%s.ParentID = %v, want nil (root)", name, *e.ParentID)
	}
}

func mustParent(t *testing.T, name string, e session.Entry, want string) {
	t.Helper()
	if e.ParentID == nil || *e.ParentID != want {
		t.Fatalf("%s.ParentID = %v, want %s", name, e.ParentID, want)
	}
}

func textOfMessage(t *testing.T, m msg.Message) string {
	t.Helper()
	switch mm := m.(type) {
	case msg.UserMessage:
		return msg.TextOf(mm.Content)
	case msg.AssistantMessage:
		return msg.TextOf(mm.Content)
	case msg.ToolResultMessage:
		return msg.TextOf(mm.Content)
	case msg.SystemMessage:
		return msg.TextOf(mm.Content)
	default:
		t.Fatalf("unexpected message type %T", m)
		return ""
	}
}

// TestUpgradeLegacyV3Atomic asserts the rewrite goes through
// "<path>.tmp" and rename (no .tmp left behind) and that a file already
// upgraded is left byte-for-byte unchanged by a second Open ("a second
// Open is a no-op").
func TestUpgradeLegacyV3Atomic(t *testing.T) {
	path := copyFixture(t, legacyV3Fixture)

	st1, err := Open(path, nil)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := st1.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("expected no .tmp file left behind, stat err = %v", err)
	}
	after1, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	st2, err := Open(path, nil)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer st2.Close()
	after2, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after1) != string(after2) {
		t.Fatal("second Open modified the already-upgraded file")
	}
	if st2.Header().V != session.FormatVersion {
		t.Fatalf("second Open header = %+v, not v4", st2.Header())
	}
}

// TestUpgradeLegacyV3CorruptRecord asserts a v3 file with an invalid record
// (bad JSON on line 2) is rejected and left completely untouched: no
// rewrite, no ".tmp" file.
func TestUpgradeLegacyV3CorruptRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.jsonl")
	content := `{"type":"session","version":3,"id":"legacy-2","cwd":"/tmp","timestamp":"2024-01-01T00:00:00.000Z"}` + "\n" +
		`{"id":"e1","parentId":null,"timestamp":"2024-01-01T00:00:01.000Z","type":"message","message":{not valid json` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := UpgradeLegacyV3(path, nil); err == nil {
		t.Fatal("expected UpgradeLegacyV3 to fail on invalid JSON")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("corrupt legacy v3 file was modified")
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("expected no .tmp file left behind, stat err = %v", err)
	}
}

// TestUpgradeLegacyV3TornTail asserts a v3 file whose last line has no
// trailing newline (a writer caught mid-append) is tolerated like pi
// tolerates it (legacy-v3.js:270-273, "if (!line.terminated) break") rather
// than treated as corrupt: the torn line is dropped, and every complete
// line before it upgrades normally.
func TestUpgradeLegacyV3TornTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.jsonl")
	content := `{"type":"session","version":3,"id":"legacy-4","cwd":"/tmp","timestamp":"2024-01-01T00:00:00.000Z"}` + "\n" +
		`{"id":"e1","parentId":null,"timestamp":"2024-01-01T00:00:01.000Z","type":"message","message":{"role":"user","content":"hi","timestamp":1704067201000}}` + "\n" +
		`{"id":"e2","parentId":"e1","timestamp":"2024-01-01T00:00:02.000Z","type":"message","message":{` // torn: no closing content, no trailing newline
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := Open(path, nil)
	if err != nil {
		t.Fatalf("Open with a torn tail: %v", err)
	}
	defer st.Close()
	entries := st.ScanEntries(session.EntryScan{Order: "asc"})
	if len(entries) != 1 {
		t.Fatalf("ScanEntries returned %d entries, want 1 (e2's torn line dropped)", len(entries))
	}
}

// TestUpgradeLegacyV3RejectsCustomMessage documents (via a real assertion)
// the one v3 record type this port refuses rather than upgrades: see the
// deviation note at the top of legacy_v3.go.
func TestUpgradeLegacyV3RejectsCustomMessage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.jsonl")
	content := `{"type":"session","version":3,"id":"legacy-3","cwd":"/tmp","timestamp":"2024-01-01T00:00:00.000Z"}` + "\n" +
		`{"id":"e1","parentId":null,"timestamp":"2024-01-01T00:00:01.000Z","type":"custom_message","customType":"note","content":"hi","display":true}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := UpgradeLegacyV3(path, nil); err == nil {
		t.Fatal("expected UpgradeLegacyV3 to reject a custom_message record")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("rejected legacy v3 file was modified")
	}
}
