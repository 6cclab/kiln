package tools

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/execenv"
)

// TestEditAndWriteRefuseAFileChangedSinceRead: like Claude Code, a file
// that changed after the session read it (the person edited it, a
// formatter ran) is refused with Claude Code's message until it is read
// again, so a stale edit never overwrites the newer content. The
// session's own edits and writes keep its view current.
func TestEditAndWriteRefuseAFileChangedSinceRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	put := func(s string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := execenv.New(dir)
	read, edit, write := ReadTool(env), EditTool(env), WriteTool(env)
	editArgs := func(old, new string) map[string]any {
		return map[string]any{"path": "f.txt", "edits": []map[string]string{{"oldText": old, "newText": new}}}
	}

	put("one\ntwo\n")
	execTool(t, read, map[string]any{"path": "f.txt"})
	if r := execTool(t, edit, editArgs("one", "uno")); r.IsError {
		t.Fatalf("edit after read: %s", resultText(r))
	}
	if r := execTool(t, edit, editArgs("two", "dos")); r.IsError {
		t.Fatalf("second edit after the session's own edit: %s", resultText(r))
	}

	put("uno\ndos\nthree, added by the person\n")
	r := execTool(t, edit, editArgs("uno", "1"))
	if !r.IsError || resultText(r) != execenv.StaleFileMessage {
		t.Fatalf("edit of a changed file = %q, want Claude Code's refusal", resultText(r))
	}
	r = execTool(t, write, map[string]any{"path": "f.txt", "content": "overwrite"})
	if !r.IsError || resultText(r) != execenv.StaleFileMessage {
		t.Fatalf("write over a changed file = %q, want the refusal", resultText(r))
	}
	if data, _ := os.ReadFile(path); string(data) != "uno\ndos\nthree, added by the person\n" {
		t.Fatalf("the person's change was overwritten: %q", data)
	}

	execTool(t, read, map[string]any{"path": "f.txt"})
	if r := execTool(t, write, map[string]any{"path": "f.txt", "content": "fresh\n"}); r.IsError {
		t.Fatalf("write after reading again: %s", resultText(r))
	}
}

// TestPartialReadAndUnreadFiles: after an offset/limit read only the size
// and mtime can show a change, so they are checked; a file the session
// never read has nothing to compare and is edited as before.
func TestPartialReadAndUnreadFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := execenv.New(dir)
	edit := EditTool(env)
	args := map[string]any{"path": "f.txt", "edits": []map[string]string{{"oldText": "a", "newText": "A"}}}
	if r := execTool(t, edit, args); r.IsError {
		t.Fatalf("edit of a never-read file: %s", resultText(r))
	}

	execTool(t, ReadTool(env), map[string]any{"path": "f.txt", "offset": 2, "limit": 1})
	if err := os.WriteFile(path, []byte("A\nb\nc\nd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	args = map[string]any{"path": "f.txt", "edits": []map[string]string{{"oldText": "b", "newText": "B"}}}
	if r := execTool(t, edit, args); !r.IsError || resultText(r) != execenv.StaleFileMessage {
		t.Fatalf("edit after a partial read and a change = %q, want the refusal", resultText(r))
	}
}
