package execenv

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func scanAll(t *testing.T, s *LineScanner) []ScanLine {
	t.Helper()
	var out []ScanLine
	for {
		l, ok, err := s.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if !ok {
			return out
		}
		out = append(out, l)
	}
}

func TestLineScannerBasic(t *testing.T) {
	s := NewLineScanner(strings.NewReader("a\nbb\nccc"), 0)
	got := scanAll(t, s)
	want := []ScanLine{{Content: "a", ByteLen: 1}, {Content: "bb", ByteLen: 2}, {Content: "ccc", ByteLen: 3}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestLineScannerTrailingNewlineNoPhantomLine(t *testing.T) {
	// Mirrors splitLinesForCounting's rule: a trailing newline does not
	// produce a trailing empty line.
	s := NewLineScanner(strings.NewReader("a\nb\n"), 0)
	got := scanAll(t, s)
	if len(got) != 2 || got[0].Content != "a" || got[1].Content != "b" {
		t.Errorf("got %+v, want [a b]", got)
	}
}

func TestLineScannerCRLF(t *testing.T) {
	s := NewLineScanner(strings.NewReader("a\r\nb"), 0)
	got := scanAll(t, s)
	if len(got) != 2 || got[0].Content != "a" || got[1].Content != "b" {
		t.Errorf("got %+v, want [a b]", got)
	}
}

func TestLineScannerEmptyInput(t *testing.T) {
	s := NewLineScanner(strings.NewReader(""), 0)
	if _, ok, err := s.Next(); ok || err != nil {
		t.Errorf("Next on empty input: ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}

func TestLineScannerOversizeLineIsCappedNotDropped(t *testing.T) {
	s := NewLineScanner(strings.NewReader("abcdef\nxyz"), 3)
	got := scanAll(t, s)
	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2: %+v", len(got), got)
	}
	if !got[0].Oversize || got[0].Content != "abc" || got[0].ByteLen != 6 {
		t.Errorf("line 1 = %+v, want {Content:abc ByteLen:6 Oversize:true}", got[0])
	}
	if got[1].Oversize || got[1].Content != "xyz" || got[1].ByteLen != 3 {
		t.Errorf("line 2 = %+v, want {Content:xyz ByteLen:3 Oversize:false}", got[1])
	}
}

// infiniteReader generates bytes on demand rather than ever holding them
// all at once, standing in for a hostile huge single line (no newline)
// without this test allocating one; wrapped in io.LimitReader so Next
// still terminates.
type infiniteReader struct{ b byte }

func (r infiniteReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.b
	}
	return len(p), nil
}

// A single oversize line — here, 50 MB with no newline at all, generated
// rather than held as a literal — is capped at maxBytes exactly like a
// short oversize line: Next still returns promptly and its Content is
// never more than maxBytes, proving the cap holds regardless of how long
// the real line actually is.
func TestLineScannerHugeSingleLineStaysCapped(t *testing.T) {
	// The property under test is memory, not speed: one 50MB line must be
	// drained for its length but never held past the cap. The input is
	// itself bounded (LimitReader), so a regression costs at most 50MB and
	// is caught by the allocation check below, not by a wall-clock deadline
	// that the race detector can blow on a slow CI runner.
	const lineLen = 50 << 20
	r := io.LimitReader(infiniteReader{b: 'a'}, lineLen)
	s := NewLineScanner(r, DefaultMaxBytes)

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	l, ok, err := s.Next()
	runtime.ReadMemStats(&after)

	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("Next returned no line")
	}
	if !l.Oversize {
		t.Error("want Oversize for a line far longer than maxBytes")
	}
	if l.ByteLen != lineLen {
		t.Errorf("ByteLen = %d, want %d", l.ByteLen, lineLen)
	}
	if len(l.Content) != DefaultMaxBytes {
		t.Errorf("len(Content) = %d, want the %d-byte cap", len(l.Content), DefaultMaxBytes)
	}
	// Holding the whole line would allocate at least lineLen bytes; a capped
	// scan allocates a small multiple of the cap plus read buffers.
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > lineLen/8 {
		t.Errorf("scanning one %dMB line allocated %d bytes; want well under %d (the line must not be held whole)", lineLen>>20, alloc, lineLen/8)
	}
	if _, ok, err := s.Next(); ok || err != nil {
		t.Errorf("second Next: ok=%v err=%v, want ok=false err=nil (reader exhausted)", ok, err)
	}
}

// within fails the test unless fn returns within the deadline — mirrors
// internal/gitfiles/hostile_test.go's own within, duplicated here rather
// than exported across packages for a test-only helper.
func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		fmt.Fprintf(os.Stderr, "--- FAIL: %s: %s did not return within ten seconds; exiting so the read cannot keep allocating\n", t.Name(), what)
		os.Exit(1)
	}
}

func TestLineScannerLargeFileStaysBounded(t *testing.T) {
	// A real, finite, regular file of plain lines, large enough that
	// holding it all in memory at once (as a single os.ReadFile +
	// strings.Split, read.go's shape before this fix) would be the thing
	// under test — but LineScanner must terminate well inside the
	// deadline either way, proving it is not quietly doing an O(file)
	// allocation to get there.
	dir := t.TempDir()
	path := dir + "/big.txt"
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	const lines = 200_000
	w := []byte("the quick brown fox jumps over the lazy dog\n")
	for i := 0; i < lines; i++ {
		if _, err := f.Write(w); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	within(t, "scanning a 200,000-line file", func() {
		rf, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer rf.Close()
		s := NewLineScanner(rf, DefaultMaxBytes)
		count := 0
		for {
			_, ok, err := s.Next()
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				break
			}
			count++
		}
		if count != lines {
			t.Errorf("counted %d lines, want %d", count, lines)
		}
	})
}

var _ io.Reader = infiniteReader{}
