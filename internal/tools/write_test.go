package tools

import (
	"os"
	"path/filepath"
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
