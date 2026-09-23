// Package stubtui is a minimal Bubbletea v2 program built for testkit/screen
// and cmd/harness-drive to exercise against.
//
// It deliberately keeps the parts of a real TUI that phase 0 needs to verify
// and nothing else:
//
//   - a fixed "live region" (an input line with a "❯" prompt, plus a 2-row
//     footer) that is always present at the bottom of the terminal;
//   - tea.Println output that scrolls into real scrollback above the live
//     region, so committed lines can be told apart from the live region;
//   - a tall overlay panel (taller than the live region) that splices over
//     the view, to exercise the shrink-band regression when it closes;
//   - the last key pressed, echoed into the footer via KeyPressMsg.String(),
//     so key encodings can be asserted end to end through a real PTY.
//
// It does not set AltScreen and does not request ReportEventTypes.
package stubtui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// overlayHeight is the height of the overlay panel in rows. It is
// deliberately taller than the live region (3 rows: 1 input + 2 footer) so
// that opening it grows the occupied height and closing it must give the
// rows back.
const overlayHeight = 12

// Run starts the stub program and blocks until it quits. It returns a
// process exit code: 0 on a clean quit, 1 on error.
func Run(_ []string) int {
	p := tea.NewProgram(initialModel())
	if _, err := p.Run(); err != nil {
		fmt.Println("stubtui: error:", err)
		return 1
	}
	return 0
}

type model struct {
	committed int
	lastKey   string
	overlay   bool
}

func initialModel() model {
	return model{}
}

func (m model) Init() tea.Cmd {
	return nil
}

// clearScreenCmd forces bubbletea's renderer to erase and fully redraw on
// the next flush.
//
// It is required here, and it is a phase 0 finding worth keeping in the Go
// port: charm.land/bubbletea/v2 v2.0.9's inline (non-AltScreen) renderer
// does not correctly reflow the screen when View()'s rendered height shrinks
// a lot in one step (see internal/testkit/screen's overlay test and its
// report for the byte-level evidence). Without this Cmd, closing the
// overlay left the top ~9 rows of the old 12-row panel on screen and wrote
// the new 3-row live region into the wrong rows entirely, reproducing
// exactly the "shrink-band regression" docs/testing.md describes from the
// TypeScript project's pi-tui. Sending tea.ClearScreen() whenever a view
// transition is about to shrink the frame works around it.
func clearScreenCmd() tea.Msg { return tea.ClearScreen() }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		m.lastKey = msg.String()
		switch msg.String() {
		case "q":
			return m, tea.Quit
		case "p":
			m.committed++
			n := m.committed
			return m, tea.Println(fmt.Sprintf("committed line %d", n))
		case "o":
			wasOpen := m.overlay
			m.overlay = !m.overlay
			if wasOpen {
				return m, clearScreenCmd
			}
		case "esc":
			if m.overlay {
				m.overlay = false
				return m, clearScreenCmd
			}
		}
	}
	return m, nil
}

func (m model) View() tea.View {
	if m.overlay {
		v := tea.NewView(m.overlayView())
		v.Cursor = tea.NewCursor(0, overlayHeight-1)
		return v
	}
	v := tea.NewView(m.liveRegionView())
	v.Cursor = tea.NewCursor(2, 0)
	return v
}

// liveRegionView renders the fixed 3-row live region: one input line with a
// "❯" prompt, followed by a 2-row footer. The footer's first line echoes the
// last key pressed so key encodings can be asserted; the second line is a
// fixed status line.
func (m model) liveRegionView() string {
	lines := []string{
		"❯ ",
		fmt.Sprintf("key: %s", m.lastKey),
		fmt.Sprintf("committed: %d", m.committed),
	}
	return strings.Join(lines, "\n")
}

// overlayView renders a panel taller than the live region, spliced over the
// view entirely while open.
func (m model) overlayView() string {
	lines := make([]string, overlayHeight)
	for i := range lines {
		lines[i] = fmt.Sprintf("overlay row %d", i)
	}
	return strings.Join(lines, "\n")
}
