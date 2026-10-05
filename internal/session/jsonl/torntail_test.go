package jsonl

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/session"
)

func strPtr(s string) *string { return &s }

func newTornTailHeader(id string) session.Header {
	return session.Header{
		V: session.FormatVersion, Kind: "header", ID: id,
		StorageVersion: session.StorageVersion, CreatedAt: 1000, Cwd: "/tmp/proj",
	}
}

func appendRaw(t *testing.T, path string, raw string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(raw); err != nil {
		t.Fatal(err)
	}
}

// buildTornFixture creates a session with one commit (entry-1), then
// simulates a kill mid-AppendTransaction by appending a byte stream cut
// off partway through the next commit: valid JSON prefix, no closing
// braces, no trailing newline. It returns the path and the exact bytes of
// the well-formed prefix (before the tear), for comparison.
func buildTornFixture(t *testing.T, id string) (path string, beforeTear []byte, nextSeqBeforeTear int64) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "session.jsonl")

	st, err := Create(path, newTornTailHeader(id), threeSeedWrites("main"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Commit([]session.Write{session.EntryWrite{Entry: session.Entry{ID: "entry-1", Type: session.EntryMessage}}}); err != nil {
		t.Fatal(err)
	}
	nextSeqBeforeTear = st.NextSeq()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	beforeTear, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(beforeTear, []byte("\n")) {
		t.Fatal("test fixture itself is not newline-terminated before the tear")
	}

	appendRaw(t, path, `{"kind":"entry","lane":"main","entry":{"id":"entry-2"`)

	torn, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.HasSuffix(torn, []byte("\n")) {
		t.Fatal("test fixture's torn tail is unexpectedly newline-terminated")
	}
	return path, beforeTear, nextSeqBeforeTear
}

// TestOpenLeavesTornFileUnchanged is go-audit finding 2's concurrency
// follow-up: Open must never write to the file, even to repair a torn
// tail, because another kiln process (`kiln session inspect`,
// forkSession's header read, the eval runner reading stats — none of
// which ever call Commit) can hold the same file open for append while
// this one calls Open. A premature truncate/append here could race that
// other process's own in-flight write. Open still recovers the torn
// commit in memory (nextSeq, GetEntry) — it just defers the on-disk fix to
// Commit (see TestFirstCommitRepairsTornTail).
func TestOpenLeavesTornFileUnchanged(t *testing.T) {
	path, _, nextSeqBeforeTear := buildTornFixture(t, "torn-id")
	torn, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path, nil)
	if err != nil {
		t.Fatalf("Open did not recover the torn tail: %v", err)
	}
	defer reopened.Close()

	if reopened.NextSeq() != nextSeqBeforeTear {
		t.Fatalf("nextSeq after recovery = %d, want %d", reopened.NextSeq(), nextSeqBeforeTear)
	}
	if _, ok := reopened.GetEntry("entry-1"); !ok {
		t.Fatal("entry-1 (the last complete commit) is missing after recovery")
	}
	if _, ok := reopened.GetEntry("entry-2"); ok {
		t.Fatal("the torn entry-2 write must not have been applied")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, torn) {
		t.Fatalf("Open modified the file:\nbefore=%q\nafter=%q", torn, after)
	}
}

// TestFirstCommitRepairsTornTail: the Storage from an Open that found a
// torn tail truncates the file (dropping the torn line) the first time it
// actually commits, and the new commit's own line is appended after that
// clean truncation — not after the leftover torn bytes.
func TestFirstCommitRepairsTornTail(t *testing.T) {
	path, beforeTear, _ := buildTornFixture(t, "torn-id-repair")

	st, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	res, err := st.Commit([]session.Write{session.EntryWrite{Entry: session.Entry{ID: "entry-3", ParentID: strPtr("entry-1"), Type: session.EntryMessage}}})
	if err != nil {
		t.Fatalf("first Commit after a torn-tail Open failed: %v", err)
	}
	if res.FirstSeq == 0 {
		t.Fatal("commit produced no seq")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(after, beforeTear) {
		t.Fatalf("file after first commit does not start with the pre-tear content:\nwant prefix=%q\ngot=%q", beforeTear, after)
	}
	if bytes.Contains(after, []byte("entry-2")) {
		t.Fatalf("the torn entry-2 bytes are still in the file: %q", after)
	}
	if !bytes.Contains(after, []byte("entry-3")) {
		t.Fatalf("the new commit's entry-3 was not appended: %q", after)
	}
}

// TestCommitErrorsIfFileSizeChangedSinceOpen: if the file's size changes
// between Open (which found a torn tail but deferred the fix) and this
// Storage's first Commit — e.g. another process appended to it in the
// meantime — Commit must refuse to write anything at all, rather than
// truncate or append based on a now-stale view of the file.
func TestCommitErrorsIfFileSizeChangedSinceOpen(t *testing.T) {
	path, _, _ := buildTornFixture(t, "torn-id-race")

	st, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Simulate another process finishing its own append after this
	// Storage's Open already read the file.
	appendRaw(t, path, "\n")
	racedSize, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	_, err = st.Commit([]session.Write{session.EntryWrite{Entry: session.Entry{ID: "entry-3", ParentID: strPtr("entry-1"), Type: session.EntryMessage}}})
	if err == nil {
		t.Fatal("Commit should have refused to write after the file changed size since Open")
	}
	if !strings.Contains(err.Error(), "changed since it was opened") {
		t.Fatalf("error = %v, want it to mention the file changing since it was opened", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, racedSize) {
		t.Fatalf("Commit wrote something despite refusing:\nbefore=%q\nafter=%q", racedSize, after)
	}
}

// TestOpenRecoversTornTail_FailsWithoutFix documents the pre-fix behavior:
// reverting the Open change (going back to scanner-based line-by-line
// parsing with no last-line recovery) makes this fail. It is the same
// scenario as TestOpenRecoversTornTail but asserts the OLD failure mode is
// gone, i.e. Open must not return the line-level "invalid storage" error a
// naive parse-every-line loop would produce.
func TestOpenRecoversTornTail_ErrorsReplacedByRecovery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	st, err := Create(path, newTornTailHeader("torn-id-2"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	appendRaw(t, path, `{"kind":"entry"`) // torn, unterminated

	_, err = Open(path, nil)
	if err != nil {
		t.Fatalf("Open() = %v, want nil (torn tail should be silently dropped)", err)
	}
}

// TestOpenMiddleInvalidLineStillErrors corrupts an earlier (non-final)
// transaction line in an otherwise well-formed file. Open's torn-tail
// recovery must only ever apply to the last line; a malformed line anywhere
// else is still a hard error.
func TestOpenMiddleInvalidLineStillErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	st, err := Create(path, newTornTailHeader("mid-id"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Commit([]session.Write{session.EntryWrite{Entry: session.Entry{ID: "entry-1", Type: session.EntryMessage}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Commit([]session.Write{session.EntryWrite{Entry: session.Entry{ID: "entry-2", ParentID: strPtr("entry-1"), Type: session.EntryMessage}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	physical := bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	if len(physical) != 3 {
		t.Fatalf("fixture has %d physical lines, want 3 (header + 2 commits)", len(physical))
	}
	// Corrupt the FIRST commit line (physical[1], not the last line in the
	// file) while leaving it newline-terminated.
	physical[1] = []byte("not a valid transaction at all")
	corrupted := append(bytes.Join(physical, []byte("\n")), '\n')
	if err := os.WriteFile(path, corrupted, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = Open(path, nil)
	if err == nil {
		t.Fatal("Open should fail on an invalid middle line, not silently skip or recover it")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("error = %v, want it to name line 2", err)
	}
}

// TestOpenTerminatedInvalidTailStillErrors covers both ways a final line
// can be invalid despite being newline-terminated (i.e. not torn by a
// crash): (1) the bytes are not valid JSON at all, and (2) the bytes are
// valid JSON but fail session-level validation (e.g. an entry whose parent
// does not exist). Neither should be auto-repaired: a terminated line is
// one the writer finished, so an invalid one is real corruption, not a
// crash artifact.
func TestOpenTerminatedInvalidTailStillErrors(t *testing.T) {
	t.Run("not valid JSON", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "session.jsonl")
		st, err := Create(path, newTornTailHeader("tail-json-id"), threeSeedWrites("main"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		appendRaw(t, path, "not json at all\n")

		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path, nil); err == nil {
			t.Fatal("Open must not auto-repair a terminated, syntactically invalid final line")
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("Open must not modify the file when it refuses to repair")
		}
	})

	t.Run("valid JSON but fails validation", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "session.jsonl")
		st, err := Create(path, newTornTailHeader("tail-valid-id"), threeSeedWrites("main"), nil)
		if err != nil {
			t.Fatal(err)
		}
		seq := st.NextSeq()
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		// Syntactically fine JSON, newline-terminated, but references a
		// parent entry that was never committed.
		line := fmt.Sprintf(`{"id":"entry-orphan","kind":"entry","parentId":"does-not-exist","seq":%d,"timestamp":1,"type":"message"}`+"\n", seq)

		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		appendRaw(t, path, line)
		_, err = Open(path, nil)
		if err == nil {
			t.Fatal("Open must not auto-repair a terminated line that parses but fails validation")
		}
		afterAppend, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(before, afterAppend) {
			t.Fatal("test bug: the appended line was not actually written")
		}
	})
}

// buildUnterminatedValidFixture creates a session, then appends one more,
// well-formed transaction line with no trailing "\n" — as if the write()
// that appended it landed in full and the process was killed only before
// the next AppendTransaction (or between writing the content and nothing
// else being pending).
func buildUnterminatedValidFixture(t *testing.T, id string) (path, line string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "session.jsonl")
	st, err := Create(path, newTornTailHeader(id), threeSeedWrites("main"), nil)
	if err != nil {
		t.Fatal(err)
	}
	seq := st.NextSeq()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	line = fmt.Sprintf(`{"id":"entry-ok","kind":"entry","parentId":null,"seq":%d,"timestamp":1,"type":"message"}`, seq)
	appendRaw(t, path, line) // no trailing "\n"
	return path, line
}

// TestOpenLeavesUnterminatedValidTailUnchanged covers the non-destructive
// repair side of the torn-tail fix: a final line missing only its trailing
// newline still parses and validates, so Open keeps it in memory — but,
// per the same concurrency rule as TestOpenLeavesTornFileUnchanged, Open
// does not touch the file itself to add the missing "\n".
func TestOpenLeavesUnterminatedValidTailUnchanged(t *testing.T) {
	path, _ := buildUnterminatedValidFixture(t, "complete-id")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path, nil)
	if err != nil {
		t.Fatalf("Open() = %v, want the valid-but-unterminated line to be kept", err)
	}
	defer reopened.Close()
	if _, ok := reopened.GetEntry("entry-ok"); !ok {
		t.Fatal("the unterminated-but-valid final entry should have been applied, not dropped")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("Open modified the file:\nbefore=%q\nafter=%q", before, after)
	}
	if bytes.HasSuffix(after, []byte("\n")) {
		t.Fatalf("test fixture is unexpectedly newline-terminated: %q", after)
	}
}

// TestFirstCommitCompletesUnterminatedValidTail: the Storage from an Open
// that found an unterminated-but-valid tail appends the missing "\n" the
// first time it actually commits, before appending its own new line.
func TestFirstCommitCompletesUnterminatedValidTail(t *testing.T) {
	path, line := buildUnterminatedValidFixture(t, "complete-id-commit")

	st, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if _, err := st.Commit([]session.Write{session.EntryWrite{Entry: session.Entry{ID: "entry-next", ParentID: strPtr("entry-ok"), Type: session.EntryMessage}}}); err != nil {
		t.Fatalf("first Commit after an unterminated-valid-tail Open failed: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(after, []byte(line+"\n")) {
		t.Fatalf("file after first commit does not contain the completed line %q:\ngot=%q", line+"\n", after)
	}
	if !bytes.Contains(after, []byte("entry-next")) {
		t.Fatalf("the new commit's entry-next was not appended: %q", after)
	}
}
