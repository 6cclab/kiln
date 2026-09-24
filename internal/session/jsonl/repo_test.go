package jsonl

import (
	"encoding/json"
	"os"
	"testing"
	"time"
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

// TestOpenLegacyV3Rejected asserts Open surfaces a clear error rather than
// misinterpreting a legacy v3 header.
func TestOpenLegacyV3Rejected(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/legacy.jsonl"
	content := `{"type":"session","version":3,"id":"legacy-1","cwd":"/tmp","timestamp":"2024-01-01T00:00:00.000Z"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Open(path, nil)
	if err == nil {
		t.Fatal("expected error opening legacy v3 session")
	}
	if err != ErrLegacyV3Unsupported {
		t.Fatalf("err = %v, want ErrLegacyV3Unsupported", err)
	}
}
