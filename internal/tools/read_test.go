package tools

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/msg"
)

func TestReadToolBasic(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("line1\nline2\nline3"), 0o644)
	env := execenv.New(dir)
	rt := ReadTool(env)
	result := execTool(t, rt, map[string]any{"path": "a.txt"})
	if resultText(result) != "line1\nline2\nline3" {
		t.Fatalf("text = %q", resultText(result))
	}
}

func TestReadToolOffsetLimit(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("l1\nl2\nl3\nl4\nl5"), 0o644)
	env := execenv.New(dir)
	rt := ReadTool(env)
	result := execTool(t, rt, map[string]any{"path": "a.txt", "offset": 2, "limit": 2})
	text := resultText(result)
	if !strings.HasPrefix(text, "l2\nl3") {
		t.Fatalf("text = %q", text)
	}
	if !strings.Contains(text, "2 more lines in file. Use offset=4 to continue.") {
		t.Fatalf("expected continuation hint, got %q", text)
	}
}

func TestReadToolOffsetBeyondEnd(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("l1\nl2"), 0o644)
	env := execenv.New(dir)
	rt := ReadTool(env)
	result := execTool(t, rt, map[string]any{"path": "a.txt", "offset": 10})
	if !result.IsError {
		t.Fatal("expected IsError for offset beyond end of file")
	}
	if !strings.Contains(resultText(result), "beyond end of file") {
		t.Fatalf("text = %q", resultText(result))
	}
}

func TestReadToolTruncatesLargeFile(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 0; i < 3000; i++ {
		b.WriteString("line\n")
	}
	os.WriteFile(filepath.Join(dir, "big.txt"), []byte(b.String()), 0o644)
	env := execenv.New(dir)
	rt := ReadTool(env)
	result := execTool(t, rt, map[string]any{"path": "big.txt"})
	if !strings.Contains(resultText(result), "Showing lines") {
		t.Fatalf("expected truncation footer, got tail: %q", resultText(result)[len(resultText(result))-200:])
	}
	if result.Details == nil {
		t.Fatal("expected Details on truncated read")
	}
}

func TestReadToolPNGImage(t *testing.T) {
	dir := t.TempDir()
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	// Minimal IHDR chunk header so isPNG's structural check passes: length
	// 13 (0x0D) big-endian, then "IHDR".
	png = append(png, 0x00, 0x00, 0x00, 0x0D)
	png = append(png, []byte("IHDR")...)
	png = append(png, make([]byte, 13+4)...) // IHDR payload + CRC, contents don't matter here
	os.WriteFile(filepath.Join(dir, "pic.png"), png, 0o644)
	env := execenv.New(dir)
	rt := ReadTool(env)
	result := execTool(t, rt, map[string]any{"path": "pic.png"})
	if len(result.Content) != 2 {
		t.Fatalf("expected [text, image] blocks, got %d", len(result.Content))
	}
	img, ok := result.Content[1].(msg.ImageContent)
	if !ok {
		t.Fatalf("expected ImageContent, got %T", result.Content[1])
	}
	if img.MimeType != "image/png" {
		t.Fatalf("mimeType = %q", img.MimeType)
	}
	decoded, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil || string(decoded) != string(png) {
		t.Fatalf("round-tripped image bytes do not match, err=%v", err)
	}
}

func TestReadToolMissingFile(t *testing.T) {
	env := execenv.New(t.TempDir())
	rt := ReadTool(env)
	result := execTool(t, rt, map[string]any{"path": "nope.txt"})
	if !result.IsError {
		t.Fatal("expected IsError for missing file")
	}
}
