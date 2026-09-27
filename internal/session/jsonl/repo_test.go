package jsonl

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/session"
)

const smallFixture = "../../../testdata/sessions/2026-09-23T13-15-57-415Z_01a0ce68-9c67-7740-9867-7150069d61e6.jsonl"

// TestNaming pins DirectoryName and FileName against the real paths quoted
// in the phase-1 plan.
func TestNaming(t *testing.T) {
	if got, want := DirectoryName("/Users/andrepato/projects/harness"), "--Users-andrepato-projects-harness--"; got != want {
		t.Errorf("DirectoryName = %q, want %q", got, want)
	}
	createdAt := time.Date(2026, 9, 23, 11, 37, 47, 498_000_000, time.UTC).UnixMilli()
	got := FileName(createdAt, "01a0ce0e-bcea-7701-a97e-cc374e8c56d1")
	want := "2026-09-23T11-37-47-498Z_01a0ce0e-bcea-7701-a97e-cc374e8c56d1.jsonl"
	if got != want {
		t.Errorf("FileName = %q, want %q", got, want)
	}
}

// TestCreateWritesHeaderOnly asserts Repo.Create writes only the header
// line, matching pi's repo.create. The lane-bootstrap transaction (branch
// tip / lane config / lane state) that the small fixture's second line
// shows is written by harness.Harness.Lane on a lane's first use instead
// (see internal/harness's TestLaneCreationWritesMatchFixtureShape), because
// only the harness layer knows the lane's actual model/tool configuration.
func TestCreateWritesHeaderOnly(t *testing.T) {
	repo, err := NewRepo(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	storage, meta, err := repo.Create(CreateOptions{Cwd: "/Users/tester/projects/harness"})
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()

	fixtureLines := readLines(t, smallFixture)
	if len(fixtureLines) != 2 {
		t.Fatalf("fixture has %d lines, want 2", len(fixtureLines))
	}
	gotLines := readLines(t, meta.Path)
	if len(gotLines) != 1 {
		t.Fatalf("created session has %d lines, want 1 (header only): %v", len(gotLines), gotLines)
	}

	var wantHeader, gotHeader map[string]any
	mustUnmarshal(t, fixtureLines[0], &wantHeader)
	mustUnmarshal(t, gotLines[0], &gotHeader)
	for _, key := range []string{"v", "kind", "storageVersion"} {
		if wantHeader[key] != gotHeader[key] {
			t.Errorf("header[%s] = %v, want %v", key, gotHeader[key], wantHeader[key])
		}
	}
	for _, key := range []string{"id", "cwd", "createdAt"} {
		if _, ok := gotHeader[key]; !ok {
			t.Errorf("header missing key %q", key)
		}
	}

	// And the metadata/listing path works end to end.
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	listed, err := repo.List(meta.Cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != meta.ID {
		t.Fatalf("List = %+v, want one entry with id %s", listed, meta.ID)
	}
}

// TestListFindsSessionAcrossSymlinkedCwdSpellings guards against sessions
// splitting into two projects when the same directory is reached through
// two different spellings of a path (e.g. macOS's /tmp being a symlink to
// /private/tmp): a session created via one spelling must still be found
// when queried via the other, and querying by either spelling must find
// sessions recorded under either.
func TestListFindsSessionAcrossSymlinkedCwdSpellings(t *testing.T) {
	root := t.TempDir()
	repo, err := NewRepo(root)
	if err != nil {
		t.Fatal(err)
	}

	real := t.TempDir()
	linkParent := t.TempDir()
	link := linkParent + "/proj-link"
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	// A session created via the real (already-resolved) path.
	storage1, meta1, err := repo.Create(CreateOptions{Cwd: real})
	if err != nil {
		t.Fatal(err)
	}
	if err := storage1.Close(); err != nil {
		t.Fatal(err)
	}

	// Another session created via the symlinked spelling. Before the fix,
	// this stored the literal symlinked path and bucketed into a
	// different DirectoryName than the one above, splitting one project's
	// history into two.
	storage2, meta2, err := repo.Create(CreateOptions{Cwd: link})
	if err != nil {
		t.Fatal(err)
	}
	if err := storage2.Close(); err != nil {
		t.Fatal(err)
	}

	// Querying by either spelling must find both sessions.
	for _, query := range []string{real, link} {
		listed, err := repo.List(query)
		if err != nil {
			t.Fatalf("List(%q): %v", query, err)
		}
		if len(listed) != 2 {
			t.Fatalf("List(%q) = %d sessions, want 2 (got %+v)", query, len(listed), listed)
		}
		ids := map[string]bool{listed[0].ID: true, listed[1].ID: true}
		if !ids[meta1.ID] || !ids[meta2.ID] {
			t.Fatalf("List(%q) = %+v, want both %s and %s", query, listed, meta1.ID, meta2.ID)
		}
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, string(data[start:i]))
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, string(data[start:]))
	}
	return lines
}

func mustUnmarshal(t *testing.T, s string, out any) {
	t.Helper()
	if err := json.Unmarshal([]byte(s), out); err != nil {
		t.Fatalf("unmarshal %q: %v", s, err)
	}
}

// TestListAndDelete exercises the basic repo lifecycle beyond create.
func TestListAndDelete(t *testing.T) {
	repo, err := NewRepo(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	storage, meta, err := repo.Create(CreateOptions{Cwd: "/proj/a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	list, err := repo.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("List = %+v", list)
	}
	if err := repo.Delete(meta); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(meta.Path); !os.IsNotExist(err) {
		t.Fatalf("expected session file removed, stat err = %v", err)
	}
}

// TestOpenLegacyV3HeaderOnlyUpgrades asserts Open on a legacy v3 file (even
// a degenerate header-only one, with zero v3 records) upgrades it to v4 in
// place rather than rejecting it, and that the result opens like any other
// v4 session. See legacy_v3.go for why Open upgrades eagerly rather than
// leaving the file legacy until a commit, unlike pi.
func TestOpenLegacyV3HeaderOnlyUpgrades(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/legacy.jsonl"
	content := `{"type":"session","version":3,"id":"legacy-1","cwd":"/tmp","timestamp":"2024-01-01T00:00:00.000Z"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path, nil)
	if err != nil {
		t.Fatalf("Open legacy v3 header-only file: %v", err)
	}
	defer st.Close()
	if st.Header().V != session.FormatVersion {
		t.Fatalf("header v = %d, want %d", st.Header().V, session.FormatVersion)
	}
	if st.Header().ID != "legacy-1" {
		t.Fatalf("header id = %q", st.Header().ID)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := ParseHeader(strings.SplitN(string(raw), "\n", 2)[0])
	if err != nil {
		t.Fatal(err)
	}
	if first.Format != FormatV4 {
		t.Fatalf("on-disk header format = %v, want v4", first.Format)
	}

	// A header-only v3 file has no model_change, so no configuration is
	// derivable and pi.lane.config/pi.lane.state are not written — only the
	// branch tip (null, since there is no final entry) and the usage
	// adjustment row.
	tipRaw, _, ok := st.GetValue(session.NamespaceBranchTip, "main")
	if !ok {
		t.Fatal("missing pi.branch.tip/main after upgrade")
	}
	tip, err := session.GetTypedValue[*string](tipRaw)
	if err != nil {
		t.Fatal(err)
	}
	if tip != nil {
		t.Fatalf("branch tip = %v, want nil", tip)
	}
	if _, _, ok := st.GetValue(session.NamespaceLaneConfig, "main"); ok {
		t.Fatal("pi.lane.config/main should not exist: no model_change in source")
	}
}

// TestOpenLegacyV3Malformed asserts a genuinely corrupt v3 file (an unknown
// record type on line 2) is rejected without modifying the file.
func TestOpenLegacyV3Malformed(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/legacy.jsonl"
	content := `{"type":"session","version":3,"id":"legacy-1","cwd":"/tmp","timestamp":"2024-01-01T00:00:00.000Z"}` + "\n" +
		`{"id":"e1","parentId":null,"timestamp":"2024-01-01T00:00:01.000Z","type":"not_a_real_type"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Open(path, nil)
	if err == nil {
		t.Fatal("expected error opening malformed legacy v3 session")
	}
	if !errors.Is(err, ErrLegacyV3Unsupported) {
		t.Fatalf("err = %v, want ErrLegacyV3Unsupported", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("malformed legacy v3 file was modified")
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("expected no .tmp file left behind, stat err = %v", err)
	}
}
