package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/commands"
)

// buildPopupSync is BuildPopup with the index build run inline, for tests
// that care about the result rather than the timing.
func buildPopupSync(reg *commands.Registry, cwd, line string, col int) *Popup {
	x := newFileIndex()
	p, cmd := BuildPopup(reg, cwd, line, col, x)
	if cmd == nil {
		return p
	}
	x.apply(cmd().(msgFileIndexReady))
	p, _ = BuildPopup(reg, cwd, line, col, x)
	return p
}

// typeText feeds s to the model one key at a time, the way the terminal does.
func typeText(m Model, s string) (Model, []tea.Cmd) {
	var cmds []tea.Cmd
	for _, r := range s {
		next, cmd := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = next.(Model)
		cmds = append(cmds, cmd)
	}
	return m, cmds
}

// TestRefreshPopup_NeverWaitsOnTheWalk pins that typing after "@" returns
// while the directory walk is still running, and that the popup fills in
// when the index lands.
func TestRefreshPopup_NeverWaitsOnTheWalk(t *testing.T) {
	m := newTestModel()
	m.cfg.Cwd = t.TempDir()
	m.files = newFileIndex()
	release := make(chan struct{})
	m.files.walk = func(string) []fileCandidate {
		<-release
		return []fileCandidate{{relPath: "foo.go"}}
	}

	start := time.Now()
	m, cmds := typeText(m, "@fo")
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("typing took %v with the walk blocked; Update must not wait on it", d)
	}
	if m.popup != nil {
		t.Fatalf("popup shown before any index exists: %+v", m.popup)
	}
	var build tea.Cmd
	for _, c := range cmds {
		if c != nil {
			build = c
			break
		}
	}
	if build == nil {
		t.Fatal("no build command returned for the first @")
	}

	done := make(chan tea.Msg, 1)
	go func() { done <- build() }()
	close(release)
	ready := <-done
	next, _ := m.Update(ready)
	m = next.(Model)
	if m.popup == nil || len(m.popup.Items) != 1 || m.popup.Items[0].Value != "foo.go" {
		t.Fatalf("popup after index landed = %+v, want foo.go", m.popup)
	}
}

// TestFileIndex_StaleResultDiscarded: a walk started before an invalidation
// (a tool edited files) must not replace the newer one's result.
func TestFileIndex_StaleResultDiscarded(t *testing.T) {
	x := newFileIndex()
	call := 0
	x.walk = func(string) []fileCandidate {
		call++
		return []fileCandidate{{relPath: fmt.Sprintf("v%d.txt", call)}}
	}
	_, c1 := x.lookup("/base")
	old := c1().(msgFileIndexReady)
	x.invalidate()
	_, c2 := x.lookup("/base")
	if c2 == nil {
		t.Fatal("no rebuild after invalidate")
	}
	if x.apply(old) {
		t.Fatal("stale result was applied")
	}
	if !x.apply(c2().(msgFileIndexReady)) {
		t.Fatal("current result was dropped")
	}
	got, c3 := x.lookup("/base")
	if c3 != nil || len(got) != 1 || got[0].relPath != "v2.txt" {
		t.Fatalf("index = %+v (rebuild %v), want only v2.txt and no rebuild", got, c3 != nil)
	}
}

// TestFileIndex_ServesStaleWhileRebuilding: after the TTL the old list keeps
// answering while one rebuild runs.
func TestFileIndex_ServesStaleWhileRebuilding(t *testing.T) {
	x := newFileIndex()
	now := time.Unix(1000, 0)
	x.now = func() time.Time { return now }
	x.walk = func(string) []fileCandidate { return []fileCandidate{{relPath: "a"}} }
	_, c := x.lookup("/b")
	x.apply(c().(msgFileIndexReady))
	if _, c := x.lookup("/b"); c != nil {
		t.Fatal("fresh index triggered a rebuild")
	}
	now = now.Add(fileIndexTTL + time.Second)
	got, c := x.lookup("/b")
	if c == nil || len(got) != 1 {
		t.Fatalf("after TTL: entries=%d rebuild=%v, want old entries and a rebuild", len(got), c != nil)
	}
	if _, c2 := x.lookup("/b"); c2 != nil {
		t.Fatal("second lookup started a duplicate rebuild")
	}
}

// makeTree writes dirs*perDir files under root.
func makeTree(tb testing.TB, root string, dirs, perDir int) {
	tb.Helper()
	for d := 0; d < dirs; d++ {
		sub := filepath.Join(root, fmt.Sprintf("d%03d", d))
		if err := os.MkdirAll(sub, 0o755); err != nil {
			tb.Fatal(err)
		}
		for f := 0; f < perDir; f++ {
			if err := os.WriteFile(filepath.Join(sub, fmt.Sprintf("f%03d.txt", f)), nil, 0o644); err != nil {
				tb.Fatal(err)
			}
		}
	}
}

// TestFileSuggestions_FindsFileBeyondOldCutoff: the old walk stopped at 4,000
// entries, so a file sorted after them was never suggested.
func TestFileSuggestions_FindsFileBeyondOldCutoff(t *testing.T) {
	dir := t.TempDir()
	makeTree(t, dir, 50, 100)
	if err := os.WriteFile(filepath.Join(dir, "zz_needle.go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	p := buildPopupSync(nil, dir, "@needle", 7)
	if p == nil || len(p.Items) == 0 || p.Items[0].Value != "zz_needle.go" {
		t.Fatalf("popup = %+v, want zz_needle.go (5,000+ entries precede it)", p)
	}
}

// BenchmarkAtKeystroke measures one keystroke after "@" on a ~50k-file tree
// with the index warm (the steady state while typing).
func BenchmarkAtKeystroke(b *testing.B) {
	dir := b.TempDir()
	makeTree(b, dir, 250, 200)
	m := newTestModel()
	m.cfg.Cwd = dir
	m, cmds := typeText(m, "@")
	for _, c := range cmds {
		if c != nil {
			next, _ := m.Update(c())
			m = next.(Model)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		next, _ := m.Update(tea.KeyPressMsg{Code: 'f', Text: "f"})
		m2 := next.(Model)
		next, _ = m2.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
		m = next.(Model)
	}
}
