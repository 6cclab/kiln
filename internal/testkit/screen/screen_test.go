package screen_test

import (
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/testkit/screen"
)

// TestGoldenInitialFrame pins the stub's initial frame at 60x12: a prompt
// line followed by the two-line footer, nothing else.
func TestGoldenInitialFrame(t *testing.T) {
	s := screen.Start(t, stubBinary, nil, 60, 12)
	if err := s.WaitFor("❯", 5*time.Second); err != nil {
		t.Fatalf("waiting for prompt: %v", err)
	}
	s.Golden(t, "stubtui-initial-60x12")
}

// TestPrintlnScrollsAboveLiveRegion answers phase 0 question (i): once more
// lines are committed than the screen has rows, do committed lines leave the
// viewport into scrollback while the live region (input + 2-row footer)
// stays pinned at the bottom, with the cursor staying inside it and the
// occupied height staying constant?
func TestPrintlnScrollsAboveLiveRegion(t *testing.T) {
	const cols, rows = 40, 10
	s := screen.Start(t, stubBinary, nil, cols, rows)
	if err := s.WaitFor("❯", 5*time.Second); err != nil {
		t.Fatalf("waiting for prompt: %v", err)
	}

	// Fill the screen with committed lines until it is full.
	for i := 0; i < rows; i++ {
		s.SendKey("p")
	}
	if err := s.WaitFor("committed line", 5*time.Second); err != nil {
		t.Fatalf("waiting for committed lines: %v", err)
	}
	// Let the frame settle.
	time.Sleep(100 * time.Millisecond)

	occBefore := s.OccupiedHeight()
	cursorBefore := s.CursorRow()
	if occBefore != rows {
		t.Fatalf("expected the screen to be full (occupied height %d), got %d\n%v", rows, occBefore, s.Viewport())
	}
	// The live region is 3 rows (1 input + 2 footer); the cursor must sit
	// inside it, pinned near the bottom.
	if cursorBefore < rows-3 {
		t.Fatalf("cursor at row %d, expected it inside the live region (rows %d-%d)", cursorBefore, rows-3, rows-1)
	}

	// Overflow well past the screen height so committed lines must scroll
	// into real scrollback.
	for i := 0; i < rows*3; i++ {
		s.SendKey("p")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if len(s.Scrollback()) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scrollback never grew after overflowing the screen")
		}
		time.Sleep(15 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)

	occAfter := s.OccupiedHeight()
	cursorAfter := s.CursorRow()

	if occAfter != occBefore {
		t.Errorf("occupied height changed after overflow: before=%d after=%d\n%v", occBefore, occAfter, s.Viewport())
	}
	if cursorAfter != cursorBefore {
		t.Errorf("cursor row changed after overflow: before=%d after=%d", cursorBefore, cursorAfter)
	}
	if len(s.Scrollback()) == 0 {
		t.Errorf("expected scrollback to be non-empty after overflowing the screen")
	}
}

// TestOverlayGrowsAndShrinks answers phase 0 question (ii): opening the tall
// overlay must grow the occupied height, and closing it must return both the
// occupied height and the cursor row to their pre-open values -- this is the
// shrink-band regression from docs/testing.md ("a panel that blanks its rows
// with ESC[2K and a panel that gives them back produce different screens").
func TestOverlayGrowsAndShrinks(t *testing.T) {
	const cols, rows = 40, 20
	s := screen.Start(t, stubBinary, nil, cols, rows)
	if err := s.WaitFor("❯", 5*time.Second); err != nil {
		t.Fatalf("waiting for prompt: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Commit one line first. Closing the overlay must give the rows back
	// without erasing what was already committed above the live region;
	// tea.ClearScreen reclaims rows but wipes this line, which is why the
	// renderer is patched instead (third_party/ultraviolet).
	s.SendKey("p")
	if err := s.WaitFor("committed line 1", 5*time.Second); err != nil {
		t.Fatalf("waiting for committed line: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	occBefore := s.OccupiedHeight()
	cursorBefore := s.CursorRow()
	if occBefore != 4 {
		t.Fatalf("expected one committed line plus a 3-row live region, got %d\n%v", occBefore, s.Viewport())
	}

	s.SendKey("o")
	if err := s.WaitFor("overlay row 11", 5*time.Second); err != nil {
		t.Fatalf("waiting for overlay: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	occOpen := s.OccupiedHeight()
	if occOpen <= occBefore {
		t.Fatalf("expected opening the overlay to grow occupied height past %d, got %d", occBefore, occOpen)
	}
	if occOpen != occBefore-3+12 {
		t.Errorf("expected the overlay to occupy 12 rows above the committed line, got %d\n%v", occOpen, s.Viewport())
	}

	s.SendKey("escape")
	if err := s.WaitFor("❯", 5*time.Second); err != nil {
		t.Fatalf("waiting for prompt after closing overlay: %v", err)
	}
	time.Sleep(150 * time.Millisecond)

	occClosed := s.OccupiedHeight()
	cursorClosed := s.CursorRow()
	if occClosed != occBefore {
		t.Errorf("shrink-band regression: occupied height after close = %d, want %d (pre-open)\n%v", occClosed, occBefore, s.Viewport())
	}
	if cursorClosed != cursorBefore {
		t.Errorf("shrink-band regression: cursor row after close = %d, want %d (pre-open)", cursorClosed, cursorBefore)
	}
	if !containsRow(s.Rows(), "committed line 1") {
		t.Errorf("closing the overlay erased a committed line\n%v", s.Viewport())
	}
}

func containsRow(rows []string, want string) bool {
	for _, r := range rows {
		if strings.Contains(r, want) {
			return true
		}
	}
	return false
}

// TestKeyEncodings answers part of phase 0 question (iii): sent as legacy
// bytes through a real PTY and decoded by a real Bubbletea v2 program, do
// shift+tab, ctrl+c, ctrl+r and escape come back as those exact strings from
// tea.KeyPressMsg.String()?
func TestKeyEncodings(t *testing.T) {
	cases := []struct {
		key  string
		want string
	}{
		{"shift+tab", "shift+tab"},
		{"ctrl+c", "ctrl+c"},
		{"ctrl+r", "ctrl+r"},
		{"escape", "esc"},
	}

	s := screen.Start(t, stubBinary, nil, 40, 10)
	if err := s.WaitFor("❯", 5*time.Second); err != nil {
		t.Fatalf("waiting for prompt: %v", err)
	}

	for _, c := range cases {
		s.SendKey(c.key)
		if err := s.WaitFor("key: "+c.want, 5*time.Second); err != nil {
			t.Errorf("key %q: footer never showed %q: %v", c.key, c.want, err)
		}
	}
}

// TestWidthInvariant is a sanity check that Rows()/Viewport() enforce the
// width invariant machinery itself doesn't false-positive on a well-behaved
// program.
func TestWidthInvariant(t *testing.T) {
	s := screen.Start(t, stubBinary, nil, 30, 8)
	if err := s.WaitFor("❯", 5*time.Second); err != nil {
		t.Fatalf("waiting for prompt: %v", err)
	}
	rows := s.Viewport()
	if len(rows) != 8 {
		t.Fatalf("expected 8 rows, got %d", len(rows))
	}
}

// TestExit verifies Exit waits for the process and reports its exit code.
func TestExit(t *testing.T) {
	s := screen.Start(t, stubBinary, nil, 40, 10)
	if err := s.WaitFor("❯", 5*time.Second); err != nil {
		t.Fatalf("waiting for prompt: %v", err)
	}
	s.SendKey("q")
	code, err := s.Exit()
	if err != nil {
		t.Fatalf("Exit: %v", err)
	}
	if code != 0 {
		t.Errorf("expected exit code 0, got %d", code)
	}
}

// TestStartupLatency answers phase 0 question (iii): does the stub start
// cleanly under the emulator, i.e. does the mode 2026 (synchronized output)
// capability query bubbletea sends on startup get answered promptly rather
// than stalling the first frame? It measures time from process start to the
// first frame appearing.
func TestStartupLatency(t *testing.T) {
	start := time.Now()
	s := screen.Start(t, stubBinary, nil, 80, 24)
	if err := s.WaitFor("❯", 5*time.Second); err != nil {
		t.Fatalf("waiting for first frame: %v", err)
	}
	elapsed := time.Since(start)
	t.Logf("time to first frame: %s", elapsed)
	if elapsed > time.Second {
		t.Errorf("startup took %s, expected well under 1s if capability queries are answered promptly", elapsed)
	}
}
