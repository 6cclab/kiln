package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/execenv"
)

func TestWriteToolCreatesParents(t *testing.T) {
	dir := t.TempDir()
	env := execenv.New(dir)
	wt := WriteTool(env)
	result := execTool(t, wt, map[string]any{"path": "a/b/c.txt", "content": "hello"})
	if result.IsError {
		t.Fatalf("unexpected error: %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(dir, "a/b/c.txt"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("content = %q", data)
	}
	if resultText(result) != "Successfully wrote to a/b/c.txt" {
		t.Fatalf("text = %q", resultText(result))
	}
}

func TestWriteToolOverwrites(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("old"), 0o644)
	env := execenv.New(dir)
	wt := WriteTool(env)
	execTool(t, wt, map[string]any{"path": "f.txt", "content": "new"})
	data, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if string(data) != "new" {
		t.Fatalf("content = %q", data)
	}
}

// TestWriteToolBareErrorDidYouMeanHint: write.go's WriteFile auto-creates
// missing parent directories (os.MkdirAll), so a typo'd directory name
// never reaches a bare not-found error there — it just creates a new,
// wrongly-named directory instead. The bare OS error write.go does still
// surface is os.WriteFile itself failing on a path that exists but is not
// a plain file (EISDIR here: "note.txt" is a directory). That is the one
// site this test exercises: the hint still fires, naming a no-break-space
// mismatched real file that sits next to it. (Not a case mismatch:
// macOS's default volume is case-insensitive and would make "note.txt"
// and "Note.txt" collide into one path, which is exactly what this test
// needs to avoid to isolate the EISDIR error it wants.)
func TestWriteToolBareErrorDidYouMeanHint(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "note final.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	real := "note" + " " + "final.txt"
	if err := os.WriteFile(filepath.Join(dir, real), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := execenv.New(dir)
	wt := WriteTool(env)
	result := execTool(t, wt, map[string]any{"path": "note final.txt", "content": "new"})
	if !result.IsError {
		t.Fatal("expected IsError writing to a path that is a directory")
	}
	// "note final.txt" itself is excluded from its own candidates
	// (DidYouMeanHint skips a candidate equal to the requested basename),
	// so the only match here is the deliberately-planted no-break-space
	// file.
	if !strings.Contains(resultText(result), "Did you mean") {
		t.Fatalf("text = %q, want a Did-you-mean hint", resultText(result))
	}
	if !strings.Contains(resultText(result), "U+00A0 NO-BREAK SPACE") {
		t.Fatalf("text = %q, want it to call out U+00A0", resultText(result))
	}
}
