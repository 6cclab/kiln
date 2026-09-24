package editor

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// Ported from test/history.test.ts.

func TestLoadHistoryMissingFileReturnsNil(t *testing.T) {
	dir := t.TempDir()
	got := Load(filepath.Join(dir, "absent"))
	if len(got) != 0 {
		t.Fatalf("Load() = %v, want empty", got)
	}
}

func TestHistoryRoundTripsAnEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history")
	Append(path, "fix the build")
	got := Load(path)
	want := []string{"fix the build"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %v, want %v", got, want)
	}
}

func TestHistoryKeepsMostRecentLast(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ordered")
	Append(path, "first")
	Append(path, "second")
	got := Load(path)
	want := []string{"first", "second"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %v, want %v", got, want)
	}
}

func TestHistoryKeepsMultilinePromptAsOneEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multiline")
	Append(path, "line one\nline two")
	got := Load(path)
	want := []string{"line one\nline two"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %v, want %v", got, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	nonBlank := 0
	for _, l := range strings.Split(string(data), "\n") {
		if l != "" {
			nonBlank++
		}
	}
	if nonBlank != 1 {
		t.Fatalf("file has %d non-blank lines, want 1 (multi-line entry stored as one line)", nonBlank)
	}
}

func TestHistoryAppendsRatherThanRewriting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "concurrent")
	var wg sync.WaitGroup
	for _, e := range []string{"a", "b", "c"} {
		wg.Add(1)
		go func(entry string) {
			defer wg.Done()
			Append(path, entry)
		}(e)
	}
	wg.Wait()
	if got := len(Load(path)); got != 3 {
		t.Fatalf("Load() has %d entries, want 3 (two sessions must not truncate each other)", got)
	}
}

func TestHistoryIgnoresBlankInput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blank")
	Append(path, "   ")
	Append(path, "")
	if got := Load(path); len(got) != 0 {
		t.Fatalf("Load() = %v, want empty", got)
	}
}

func TestHistoryNeverPanicsWhenUnwritable(t *testing.T) {
	Append("/nonexistent-root/nope/history", "x") // must not panic or error out loud
}

func TestHistoryPathLivesOutsideTheProject(t *testing.T) {
	p := Path()
	if !strings.Contains(p, string(filepath.Separator)+".harness"+string(filepath.Separator)) {
		t.Fatalf("Path() = %q, want it to contain /.harness/", p)
	}
}

func TestHistoryCapsAt500Entries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "many")
	for i := 0; i < 510; i++ {
		Append(path, "entry")
	}
	got := Load(path)
	if len(got) != 500 {
		t.Fatalf("Load() returned %d entries, want 500 (MAX_ENTRIES)", len(got))
	}
}
