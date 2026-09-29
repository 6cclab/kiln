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
