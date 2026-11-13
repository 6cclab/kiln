//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/testkit/screen"
)

// TestTUI_FullscreenDragCopiesSelection: fullscreen takes the mouse (the
// wheel scrolls the transcript), so the terminal cannot select text; kiln
// selects instead. A drag over a reply copies exactly the dragged text and
// says so on the mode line.
func TestTUI_FullscreenDragCopiesSelection(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	clip := filepath.Join(t.TempDir(), "clipboard.txt")
	tuiExtraOpts = []screen.Option{screen.WithEnv("HARNESS_CLIPBOARD_CMD", "cat > "+clip)}
	t.Cleanup(func() { tuiExtraOpts = nil })
	addr, _ := startFaux(t, "model: faux-1\nsteps:\n  - text: \"alpha bravo charlie delta\"\n")
	s := startTUI(t, 100, 30, proj, home, sessDir, addr, "--fullscreen")
	defer s.Close()
	waitReady(t, s)
	s.Send("hi")
	s.SendKey("enter")
	if err := s.WaitFor("alpha bravo charlie delta", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	waitTurnSettled(t, s)

	row, col := -1, -1
	for i, r := range s.Rows() {
		if c := strings.Index(r, "bravo"); c >= 0 {
			row, col = i, len([]rune(r[:c]))
			break
		}
	}
	if row < 0 {
		t.Fatalf("reply row not found:\n%s", strings.Join(s.Rows(), "\n"))
	}
	// SGR mouse: press, drag to the end of "charlie", release (1-based).
	s.Send(fmt.Sprintf("\x1b[<0;%d;%dM", col+1, row+1))
	s.Send(fmt.Sprintf("\x1b[<32;%d;%dM", col+len("bravo charlie"), row+1))
	s.Send(fmt.Sprintf("\x1b[<0;%d;%dm", col+len("bravo charlie"), row+1))
	if err := s.WaitFor("copied 13 characters", 3*time.Second); err != nil {
		t.Fatalf("no copy note: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
	got, err := os.ReadFile(clip)
	if err != nil || string(got) != "bravo charlie" {
		t.Errorf("clipboard = %q (%v), want %q", got, err, "bravo charlie")
	}
	styles := s.Styles()
	if !styles[row][col].Reverse {
		t.Errorf("selected cell (%d,%d) is not highlighted", row, col)
	}
}

// TestTUI_FullscreenClickOpensLink: fullscreen reports clicks to kiln, so
// the terminal cannot open links (and a Cmd-click arrives without its
// modifier). A click that does not drag opens the link under it: a
// markdown link's label (OSC 8) or a bare URL.
func TestTUI_FullscreenClickOpensLink(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	opened := filepath.Join(t.TempDir(), "opened.txt")
	tuiExtraOpts = []screen.Option{screen.WithEnv("HARNESS_OPEN_CMD", `printf '%s\n' "$1" >> `+opened)}
	t.Cleanup(func() { tuiExtraOpts = nil })
	addr, _ := startFaux(t, "model: faux-1\nsteps:\n  - text: \"See [PR 45](https://github.com/6cclab/ynm/pull/45) or plain https://example.com/x done\"\n")
	s := startTUI(t, 120, 30, proj, home, sessDir, addr, "--fullscreen")
	defer s.Close()
	waitReady(t, s)
	s.Send("hi")
	s.SendKey("enter")
	if err := s.WaitFor("PR 45", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	waitTurnSettled(t, s)

	click := func(text string) {
		t.Helper()
		for i, r := range s.Rows() {
			if c := strings.Index(r, text); c >= 0 {
				col := len([]rune(r[:c])) + 1
				s.Send(fmt.Sprintf("\x1b[<0;%d;%dM", col+1, i+1))
				s.Send(fmt.Sprintf("\x1b[<0;%d;%dm", col+1, i+1))
				return
			}
		}
		t.Fatalf("%q not on screen:\n%s", text, strings.Join(s.Rows(), "\n"))
	}
	click("PR 45")
	if err := s.WaitFor("opened https://github.com/6cclab/ynm/pull/45", 3*time.Second); err != nil {
		t.Fatalf("no open note: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
	click("example.com/x")
	if err := s.WaitFor("opened https://example.com/x", 3*time.Second); err != nil {
		t.Fatalf("no open note: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
	got, err := os.ReadFile(opened)
	want := "https://github.com/6cclab/ynm/pull/45\nhttps://example.com/x\n"
	if err != nil || string(got) != want {
		t.Errorf("opened = %q (%v), want %q", got, err, want)
	}
}
