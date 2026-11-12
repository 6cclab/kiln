package screen

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
)

func TestWaitForFrameSkipsPartialPaintAndPairsStyles(t *testing.T) {
	s := &Screen{cols: 40, rows: 3, emu: vt.NewSafeEmulator(40, 3), timeout: time.Second}
	write := func(data string) {
		t.Helper()
		s.emuMu.Lock()
		_, err := s.emu.Write([]byte(data))
		s.emuMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	// A renderer has painted its stable panel anchor, cleared the editor and
	// status rows, and has not yet repainted them. This is the failure frame
	// captured by the design-agents full-suite run.
	write("\x1b[1;1H2 subagents running in parallel\x1b[2;1H\x1b[2K\x1b[3;1H\x1b[2K")
	seenPartial := make(chan struct{}, 1)
	finished := make(chan struct {
		frame Frame
		err   error
	}, 1)
	go func() {
		frame, err := s.WaitForFrame(func(frame Frame) bool {
			if !strings.Contains(frame.Rows[1], "queue a follow-up") {
				select {
				case seenPartial <- struct{}{}:
				default:
				}
			}
			return strings.Contains(frame.Rows[0], "2 subagents running") &&
				strings.Contains(frame.Rows[1], "queue a follow-up") &&
				strings.Contains(frame.Rows[2], "faux/faux-1 ctx")
		}, time.Second)
		finished <- struct {
			frame Frame
			err   error
		}{frame, err}
	}()
	select {
	case <-seenPartial:
	case <-time.After(time.Second):
		t.Fatal("partial paint was not observed")
	}
	select {
	case <-finished:
		t.Fatal("partial paint accepted as a complete frame")
	default:
	}
	write("\x1b[38;2;255;0;0m\x1b[2;1Hqueue a follow-up\x1b[3;1Hfaux/faux-1 ctx")
	select {
	case result := <-finished:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.frame.Styles[1][0].Fg != "#ff0000" || result.frame.Styles[2][0].Fg != "#ff0000" {
			t.Fatalf("complete text and styles came from different paints: %+v", result.frame.Styles)
		}
	case <-time.After(time.Second):
		t.Fatal("complete frame was not returned")
	}
}
