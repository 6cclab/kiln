package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/execenv"
)

// TestEditNotFoundShowsClosestText: when old text misses (here a stale
// identifier and wrong indentation), the error keeps pi's message and adds
// the closest lines in the file with their line numbers, so the model can
// copy the real text instead of guessing again.
func TestEditNotFoundShowsClosestText(t *testing.T) {
	dir := t.TempDir()
	body := "package main\n\nfunc main() {\n\tcount := load()\n\tfmt.Println(count)\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	result := execTool(t, EditTool(execenv.New(dir)), map[string]any{
		"path":  "main.go",
		"edits": []map[string]string{{"oldText": "    total := load()\n    fmt.Println(total)", "newText": "x"}},
	})
	text := resultText(result)
	if !result.IsError || !strings.Contains(text, "Could not find the exact text in") {
		t.Fatalf("want pi's not-found error, got %q", text)
	}
	if !strings.Contains(text, "The closest text in") || !strings.Contains(text, "lines 4-5:\n\tcount := load()\n\tfmt.Println(count)\n") {
		t.Errorf("no closest-match hint with lines 4-5:\n%s", text)
	}
}

// TestEditNotFoundWithoutCloseTextAddsNoHint: unrelated text gets the
// plain error; a hint pointing at random lines would mislead.
func TestEditNotFoundWithoutCloseTextAddsNoHint(t *testing.T) {
	if _, _, ok := closestMatch("alpha\nbeta\ngamma\n", "completely different words here"); ok {
		t.Error("a hint was offered for unrelated text")
	}
	if s, e, ok := closestMatch("a\nfunc run(ctx) error {\nb\n", "func run(ctx context.Context) error {"); !ok || s != 1 || e != 2 {
		t.Errorf("closestMatch = %d,%d,%v, want line 2", s, e, ok)
	}
}
