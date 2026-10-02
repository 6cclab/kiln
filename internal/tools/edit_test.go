package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/execenv"
)

// TestEditToolFuzzyNormalization exercises every normalization
// fuzzyFindText/normalizeForFuzzyMatch applies before giving up on a
// match: smart quotes, Unicode dashes, special spaces and trailing
// whitespace per line. Each case's oldText differs from the file's actual
// bytes only in one of those ways, so a match proves that specific
// normalization is wired up.
func TestEditToolFuzzyNormalization(t *testing.T) {
	cases := []struct {
		name       string
		fileText   string
		oldText    string
		newText    string
		wantResult string
	}{
		{
			name:       "smart single quotes normalize to straight",
			fileText:   "say ‘hi’ now",
			oldText:    "say 'hi' now",
			newText:    "say bye now",
			wantResult: "say bye now",
		},
		{
			name:       "smart double quotes normalize to straight",
			fileText:   "say “hi” now",
			oldText:    `say "hi" now`,
			newText:    "say bye now",
			wantResult: "say bye now",
		},
		{
			name:       "em dash normalizes to hyphen",
			fileText:   "a—b",
			oldText:    "a-b",
			newText:    "a to b",
			wantResult: "a to b",
		},
		{
			name:       "en dash normalizes to hyphen",
			fileText:   "a–b",
			oldText:    "a-b",
			newText:    "a to b",
			wantResult: "a to b",
		},
		{
			name:       "non-breaking space normalizes to regular space",
			fileText:   "a b",
			oldText:    "a b",
			newText:    "a-and-b",
			wantResult: "a-and-b",
		},
		{
			name:       "trailing whitespace on a line is ignored",
			fileText:   "line one   \nline two",
			oldText:    "line one\nline two",
			newText:    "replaced",
			wantResult: "replaced",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "f.txt")
			if err := os.WriteFile(path, []byte(tc.fileText), 0o644); err != nil {
				t.Fatal(err)
			}
			env := execenv.New(dir)
			et := EditTool(env)
			result := execTool(t, et, map[string]any{
				"path": "f.txt",
				"edits": []map[string]string{
					{"oldText": tc.oldText, "newText": tc.newText},
				},
			})
			if result.IsError {
				t.Fatalf("unexpected error: %s", resultText(result))
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.wantResult {
				t.Fatalf("file content = %q, want %q", got, tc.wantResult)
			}
		})
	}
}

func TestEditToolCRLFPreserved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("line1\r\nline2\r\nline3"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := execenv.New(dir)
	et := EditTool(env)
	result := execTool(t, et, map[string]any{
		"path": "f.txt",
		"edits": []map[string]string{
			{"oldText": "line2", "newText": "replaced"},
		},
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", resultText(result))
	}
	got, _ := os.ReadFile(path)
	want := "line1\r\nreplaced\r\nline3"
	if string(got) != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestEditToolAmbiguousMatchErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("foo\nfoo\nfoo"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := execenv.New(dir)
	et := EditTool(env)
	result := execTool(t, et, map[string]any{
		"path": "f.txt",
		"edits": []map[string]string{
			{"oldText": "foo", "newText": "bar"},
		},
	})
	if !result.IsError {
		t.Fatal("expected IsError for an ambiguous (non-unique) match")
	}
	if !strings.Contains(resultText(result), "must be unique") {
		t.Fatalf("text = %q", resultText(result))
	}
	got, _ := os.ReadFile(path)
	if string(got) != "foo\nfoo\nfoo" {
		t.Fatalf("file was modified despite the error: %q", got)
	}
}

func TestEditToolNotFoundErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := execenv.New(dir)
	et := EditTool(env)
	result := execTool(t, et, map[string]any{
		"path": "f.txt",
		"edits": []map[string]string{
			{"oldText": "goodbye world", "newText": "x"},
		},
	})
	if !result.IsError {
		t.Fatal("expected IsError for text not found")
	}
	if !strings.Contains(resultText(result), "Could not find") {
		t.Fatalf("text = %q", resultText(result))
	}
}

func TestEditToolOverlappingEditsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := execenv.New(dir)
	et := EditTool(env)
	result := execTool(t, et, map[string]any{
		"path": "f.txt",
		"edits": []map[string]string{
			{"oldText": "abcd", "newText": "X"},
			{"oldText": "cdef", "newText": "Y"},
		},
	})
	if !result.IsError {
		t.Fatal("expected IsError for overlapping edits")
	}
	if !strings.Contains(resultText(result), "overlap") {
		t.Fatalf("text = %q", resultText(result))
	}
}

func TestEditToolMultipleEditsInOneCall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("one two three"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := execenv.New(dir)
	et := EditTool(env)
	result := execTool(t, et, map[string]any{
		"path": "f.txt",
		"edits": []map[string]string{
			{"oldText": "one", "newText": "1"},
			{"oldText": "three", "newText": "3"},
		},
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", resultText(result))
	}
	got, _ := os.ReadFile(path)
	if string(got) != "1 two 3" {
		t.Fatalf("content = %q", got)
	}
	if result.Details == nil {
		t.Fatal("expected Details with diff/patch")
	}
}

func TestEditToolNoChangeErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("same"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := execenv.New(dir)
	et := EditTool(env)
	result := execTool(t, et, map[string]any{
		"path": "f.txt",
		"edits": []map[string]string{
			{"oldText": "same", "newText": "same"},
		},
	})
	if !result.IsError {
		t.Fatal("expected IsError when the replacement is a no-op")
	}
}

func TestEditToolMissingFile(t *testing.T) {
	env := execenv.New(t.TempDir())
	et := EditTool(env)
	result := execTool(t, et, map[string]any{
		"path": "nope.txt",
		"edits": []map[string]string{
			{"oldText": "a", "newText": "b"},
		},
	})
	if !result.IsError {
		t.Fatal("expected IsError for a missing file")
	}
}

// TestEditToolMissingFileDidYouMeanHint: editing a no-break-space
// mismatch gets the same "Did you mean" hint appended to its not-found
// error. (Not a case mismatch: macOS's default volume is case-insensitive,
// so "config.yaml" and "config.YAML" would already name the same file.)
func TestEditToolMissingFileDidYouMeanHint(t *testing.T) {
	dir := t.TempDir()
	real := "config" + " " + "final.yaml"
	os.WriteFile(filepath.Join(dir, real), []byte("a: 1"), 0o644)
	env := execenv.New(dir)
	et := EditTool(env)
	result := execTool(t, et, map[string]any{
		"path":  "config final.yaml",
		"edits": []map[string]string{{"oldText": "a: 1", "newText": "a: 2"}},
	})
	if !result.IsError {
		t.Fatal("expected IsError for missing file")
	}
	if !strings.Contains(resultText(result), "Did you mean") {
		t.Fatalf("text = %q, want a Did-you-mean hint", resultText(result))
	}
	if !strings.Contains(resultText(result), "U+00A0 NO-BREAK SPACE") {
		t.Fatalf("text = %q, want it to call out U+00A0", resultText(result))
	}
}
