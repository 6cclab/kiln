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

// TestOpenRecoversTornTail simulates a kill mid-AppendTransaction: a
// well-formed file whose last append was cut off partway through, leaving
// an unterminated, unparsable final line. Open must drop exactly that line
// (truncating the file to the last complete newline) and return the
// storage as of the last complete commit, matching the fix for finding 2 of
// the go-audit ("A torn last line makes a session permanently
// unopenable").
func TestOpenRecoversTornTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	st, err := Create(path, newTornTailHeader("torn-id"), threeSeedWrites("main"), nil)
	if err != nil {
		t.Fatal(err)
	}
	entryID := "entry-1"
	if _, err := st.Commit([]session.Write{session.EntryWrite{Entry: session.Entry{ID: entryID, Type: session.EntryMessage}}}); err != nil {
		t.Fatal(err)
	}
	nextSeqBeforeTear := st.NextSeq()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(before, []byte("\n")) {
		t.Fatal("test fixture itself is not newline-terminated before the tear")
	}

	// A write() cut off mid-buffer: valid JSON prefix, no closing braces,
	// no trailing newline.
	appendRaw(t, path, `{"kind":"entry","lane":"main","entry":{"id":"entry-2"`)

	torn, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.HasSuffix(torn, []byte("\n")) {
		t.Fatal("test fixture's torn tail is unexpectedly newline-terminated")
	}

	reopened, err := Open(path, nil)
	if err != nil {
		t.Fatalf("Open did not recover the torn tail: %v", err)
	}
	defer reopened.Close()

	if reopened.NextSeq() != nextSeqBeforeTear {
		t.Fatalf("nextSeq after recovery = %d, want %d", reopened.NextSeq(), nextSeqBeforeTear)
	}
	if _, ok := reopened.GetEntry(entryID); !ok {
		t.Fatal("entry-1 (the last complete commit) is missing after recovery")
	}
	if _, ok := reopened.GetEntry("entry-2"); ok {
		t.Fatal("the torn entry-2 write must not have been applied")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("file was not truncated back to its pre-tear content:\nbefore=%q\nafter=%q", before, after)
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

// TestOpenCompletesUnterminatedButValidTail covers the non-destructive
// repair side of the same fix: a final line that is missing only its
// trailing newline (the write() that appended it landed in full; the
// process was killed before the next AppendTransaction, or between writing
// the content and nothing else was pending) still parses and validates, so
// Open keeps it and completes the file by appending the missing "\n"
// rather than dropping real, successfully-written data.
func TestOpenCompletesUnterminatedButValidTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	st, err := Create(path, newTornTailHeader("complete-id"), threeSeedWrites("main"), nil)
	if err != nil {
		t.Fatal(err)
	}
	seq := st.NextSeq()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf(`{"id":"entry-ok","kind":"entry","parentId":null,"seq":%d,"timestamp":1,"type":"message"}`, seq)
	appendRaw(t, path, line) // no trailing "\n"

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
	if !bytes.HasSuffix(after, []byte("\n")) {
		t.Fatal("Open should have completed the file by appending the missing trailing newline")
	}
	if !bytes.HasSuffix(after, []byte(line+"\n")) {
		t.Fatalf("file content after completion = %q, want it to end with %q", after, line+"\n")
	}
}
