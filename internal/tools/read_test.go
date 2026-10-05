package tools

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/msg"
)

// withinReadTest fails the test unless fn returns within the deadline: a
// refusal built from Stat alone must not actually read the file it is
// refusing, so it must be fast regardless of the file's declared size.
// Mirrors internal/gitfiles/hostile_test.go's own within.
func withinReadTest(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		fmt.Fprintf(os.Stderr, "--- FAIL: %s: %s did not return within half a second\n", t.Name(), what)
		os.Exit(1)
	}
}

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

// TestReadToolMissingFileDidYouMeanHint: a read for a no-break-space
// mismatch outside " AM."/" PM." (so ReadPathVariants' own narrow-space
// retry, which is scoped to that one pattern, does not already resolve it
// — this exercises DidYouMeanHint, not the existing variant retry) gets a
// "Did you mean" hint naming the real file appended to the bare
// not-found error. (A case-mismatch would not do here: macOS's default
// volume is case-insensitive, so "readme.txt" and "README.txt" name the
// same file and the read would simply succeed.)
func TestReadToolMissingFileDidYouMeanHint(t *testing.T) {
	dir := t.TempDir()
	real := "notes" + " " + "final.txt"
	os.WriteFile(filepath.Join(dir, real), []byte("hi"), 0o644)
	env := execenv.New(dir)
	rt := ReadTool(env)
	result := execTool(t, rt, map[string]any{"path": "notes final.txt"})
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

// An untargeted read (no offset/limit) on a file over execenv.ReadWholeFileCap
// is refused without ever reading its content — proven with a sparse file
// (Truncate sets the reported size without writing any of those bytes),
// so this test does not itself allocate anything close to the cap.
func TestReadToolRefusesOversizedUntargetedRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(execenv.ReadWholeFileCap + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	env := execenv.New(dir)
	rt := ReadTool(env)
	withinReadTest(t, "read on an oversized untargeted file", func() {
		result := execTool(t, rt, map[string]any{"path": "huge.bin"})
		if !result.IsError {
			t.Error("expected IsError for a file over ReadWholeFileCap with no offset/limit")
			return
		}
		if !strings.Contains(resultText(result), "offset/limit") {
			t.Errorf("text = %q, want it to point at offset/limit", resultText(result))
		}
	})
}

// An offset read on a file over execenv.ReadWholeFileCap must still
// succeed (and return the right window): offset/limit bypasses the
// untargeted-read cap by design, matching Claude Code's own Read tool.
func TestReadToolOffsetBypassesWholeFileCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// Comfortably over ReadWholeFileCap (256 KiB), built from short lines
	// so the line count (and so the offset exercised) is meaningful.
	line := "the quick brown fox jumps over the lazy dog\n" // 45 bytes
	lines := int(execenv.ReadWholeFileCap/int64(len(line))) + 1000
	for i := 0; i < lines; i++ {
		if _, err := f.WriteString(line); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() <= execenv.ReadWholeFileCap {
		t.Fatalf("test file is not actually over the cap: size=%v err=%v", fi, err)
	}

	env := execenv.New(dir)
	rt := ReadTool(env)
	result := execTool(t, rt, map[string]any{"path": "big.txt", "offset": lines, "limit": 1})
	if result.IsError {
		t.Fatalf("unexpected error: %q", resultText(result))
	}
	if strings.TrimSpace(resultText(result)) != strings.TrimSpace(line) {
		t.Fatalf("text = %q, want the single requested line", resultText(result))
	}
}

// Reading near the start of a file bigger than ReadWholeFileCap must
// still report the file's own total line count accurately, proving the
// streaming path counts every line, not just the ones it keeps.
func TestReadToolOffsetReportsAccurateTotalOnLargeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	const totalLines = 10_000
	for i := 1; i <= totalLines; i++ {
		if _, err := fmt.Fprintf(f, "line %d\n", i); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	env := execenv.New(dir)
	rt := ReadTool(env)
	result := execTool(t, rt, map[string]any{"path": "big.txt", "offset": 1, "limit": 3})
	text := resultText(result)
	if !strings.Contains(text, fmt.Sprintf("%d more lines in file. Use offset=4 to continue.", totalLines-3)) {
		t.Fatalf("text = %q, want the accurate remaining-lines count", text)
	}
}

// Offset beyond the end of a file is still reported (with an accurate
// total) when the file is read through the streaming window path.
func TestReadToolOffsetWindowBeyondEnd(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("l1\nl2"), 0o644)
	env := execenv.New(dir)
	rt := ReadTool(env)
	result := execTool(t, rt, map[string]any{"path": "a.txt", "offset": 10, "limit": 5})
	if !result.IsError {
		t.Fatal("expected IsError for offset beyond end of file")
	}
	if !strings.Contains(resultText(result), "beyond end of file (2 lines total)") {
		t.Fatalf("text = %q", resultText(result))
	}
}
