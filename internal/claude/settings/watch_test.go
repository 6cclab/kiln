package settings

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/claude/paths"
)

// WatchFiles reports a settings file whose content changed and then held
// still for one more poll, and nothing for the state it started with.
func TestWatchFilesReportsSettledChanges(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")
	if err := os.WriteFile(a, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got [][]string
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		WatchFiles(ctx, []string{a, b}, 10*time.Millisecond, func(changed []string) {
			mu.Lock()
			got = append(got, changed)
			mu.Unlock()
		})
		close(done)
	}()
	calls := func() [][]string {
		mu.Lock()
		defer mu.Unlock()
		return append([][]string(nil), got...)
	}
	waitFor := func(n int) [][]string {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if c := calls(); len(c) >= n {
				return c
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("calls = %v, want %d", calls(), n)
		return nil
	}

	time.Sleep(60 * time.Millisecond)
	if c := calls(); len(c) != 0 {
		t.Fatalf("reported the starting state: %v", c)
	}
	if err := os.WriteFile(b, []byte(`{"permissions":{"allow":["Bash(go test *)"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c := waitFor(1)
	if len(c[0]) != 1 || c[0][0] != b {
		t.Errorf("first change = %v, want [%s]", c[0], b)
	}
	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	c = waitFor(2)
	if len(c[1]) != 1 || c[1][0] != a {
		t.Errorf("second change = %v, want [%s]", c[1], a)
	}
	time.Sleep(60 * time.Millisecond)
	if c := calls(); len(c) != 2 {
		t.Errorf("an unchanged file was reported again: %v", c)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("WatchFiles did not return after cancel")
	}
}

// A file that differs at every poll (a save still in progress) is not
// reported until it holds still for one; a delete followed by the same
// content again (a delete-and-recreate) reports nothing.
func TestWatchStateWaitsForAWriteToSettle(t *testing.T) {
	f := filepath.Join(t.TempDir(), "s.json")
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(f, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`{}`)
	w := newWatchState([]string{f})
	for i, half := range []string{`{"permis`, `{"permissions":{"al`, `{"permissions":{"allow":[`} {
		write(half)
		if got := w.poll(); len(got) != 0 {
			t.Fatalf("poll %d reported a file still being written: %v", i, got)
		}
	}
	write(`{"permissions":{"allow":["Bash(go test *)"]}}`)
	if got := w.poll(); len(got) != 0 {
		t.Fatalf("reported on first sight: %v", got)
	}
	if got := w.poll(); len(got) != 1 || got[0] != f {
		t.Fatalf("settled change = %v, want [%s]", got, f)
	}
	if got := w.poll(); len(got) != 0 {
		t.Fatalf("reported again: %v", got)
	}
	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}
	if got := w.poll(); len(got) != 0 {
		t.Fatalf("a delete was reported before it held: %v", got)
	}
	write(`{"permissions":{"allow":["Bash(go test *)"]}}`)
	if got := w.poll(); len(got) != 0 {
		t.Fatalf("a delete-and-recreate with the same content was reported: %v", got)
	}
}

// SettingsFiles names what LoadSettings reads: the scopes asked for and
// --settings last.
func TestSettingsFilesFollowsLoadOptions(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	extra := filepath.Join(t.TempDir(), "extra.json")
	all := SettingsFiles(cwd, LoadOptions{Extra: extra})
	want := map[string]bool{
		filepath.Join(home, ".claude", "settings.json"):      true,
		filepath.Join(cwd, ".claude", "settings.json"):       true,
		filepath.Join(cwd, ".claude", "settings.local.json"): true,
	}
	seen := map[string]bool{}
	for _, f := range all {
		seen[f] = true
	}
	for f := range want {
		if !seen[f] {
			t.Errorf("SettingsFiles lacks %s: %v", f, all)
		}
	}
	if all[len(all)-1] != extra {
		t.Errorf("--settings file is not last: %v", all)
	}
	for _, f := range SettingsFiles(cwd, LoadOptions{Sources: []paths.Scope{paths.ScopeUser}}) {
		if strings.HasPrefix(f, cwd) {
			t.Errorf("user scope only, but SettingsFiles names %s", f)
		}
	}
}
