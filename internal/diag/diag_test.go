package diag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartWritesAndPrunes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HARNESS_LOG_DIR", dir)
	// Older logs beyond keepFiles must go; the current one must stay.
	for i := 0; i < keepFiles+5; i++ {
		name := filepath.Join(dir, "harness-20200101-000000-"+strings.Repeat("0", 2)+string(rune('a'+i%26))+".log")
		if err := os.WriteFile(name, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path, closeFn, err := Start("abc123", true)
	if err != nil {
		t.Fatal(err)
	}
	L().Info("hello", "k", "v")
	L().Debug("dbg")
	closeFn()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"msg=start", "msg=hello k=v", "msg=dbg", "msg=exit"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("log missing %q:\n%s", want, raw)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) > keepFiles {
		t.Errorf("prune left %d files, want at most %d", len(entries), keepFiles)
	}
	if Latest() != path {
		t.Errorf("Latest() = %q, want %q", Latest(), path)
	}
	// After close, logging is discarded rather than failing.
	L().Info("after close")
}

// TestPathFollowsTheRunLog: Path names the open run log, and is empty
// before Start and after its close, so a crash report never points at a
// log that is not being written.
func TestPathFollowsTheRunLog(t *testing.T) {
	t.Setenv("HARNESS_LOG_DIR", t.TempDir())
	if got := Path(); got != "" {
		t.Fatalf("Path() before Start = %q, want empty", got)
	}
	path, closeFn, err := Start("pathtest", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := Path(); got != path {
		t.Errorf("Path() = %q, want the run log %q", got, path)
	}
	closeFn()
	if got := Path(); got != "" {
		t.Errorf("Path() after close = %q, want empty", got)
	}
}
