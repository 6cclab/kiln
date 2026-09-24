package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderChangePreviewEdit(t *testing.T) {
	out := RenderChangePreview("edit", map[string]any{
		"edits": []EditOp{{OldText: "old one", NewText: "new one"}},
	})
	if out == nil {
		t.Fatal("expected a preview for an edit")
	}
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "old one") || !strings.Contains(joined, "new one") {
		t.Errorf("preview missing content: %v", out)
	}
}

func TestRenderChangePreviewEditMultiple(t *testing.T) {
	out := RenderChangePreview("edit", map[string]any{
		"edits": []EditOp{
			{OldText: "a", NewText: "b"},
			{OldText: "c", NewText: "d"},
		},
	})
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "edit 1 of 2") || !strings.Contains(joined, "edit 2 of 2") {
		t.Errorf("multi-edit index missing: %v", out)
	}
}

func TestRenderChangePreviewWriteNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.txt")
	out := RenderChangePreview("write", map[string]any{"path": path, "content": "line1\nline2"})
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "new file, 2 line(s)") {
		t.Errorf("expected new-file marker, got %v", out)
	}
}

func TestRenderChangePreviewWriteNoChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "same.txt")
	if err := os.WriteFile(path, []byte("same content"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := RenderChangePreview("write", map[string]any{"path": path, "content": "same content"})
	if len(out) != 1 || !strings.Contains(out[0], "no change") {
		t.Errorf("expected no-change marker, got %v", out)
	}
}

func TestRenderChangePreviewWriteOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.txt")
	if err := os.WriteFile(path, []byte("old content\nsecond line"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := RenderChangePreview("write", map[string]any{"path": path, "content": "new content"})
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "overwrites 2 existing line(s)") {
		t.Errorf("expected overwrite marker, got %v", out)
	}
}

func TestRenderChangePreviewClips(t *testing.T) {
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, "line")
	}
	out := RenderChangePreview("edit", map[string]any{
		"edits": []EditOp{{OldText: strings.Join(lines, "\n"), NewText: "x"}},
	})
	if len(out) != maxPreviewLines+1 {
		t.Fatalf("got %d lines, want %d (clipped + note)", len(out), maxPreviewLines+1)
	}
	if !strings.Contains(out[len(out)-1], "more line(s)") {
		t.Errorf("missing clip note: %v", out[len(out)-1])
	}
}

func TestRenderChangePreviewOtherToolsHaveNoPreview(t *testing.T) {
	if out := RenderChangePreview("bash", map[string]any{"command": "ls"}); out != nil {
		t.Errorf("bash should have no preview, got %v", out)
	}
}
