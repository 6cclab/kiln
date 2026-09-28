package tui

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Fullscreen mode reports mouse events to kiln (the wheel scrolls the
// transcript), which takes click-and-drag away from the terminal's own
// selection. kiln selects instead: dragging over the transcript highlights
// cells, and releasing copies them to the clipboard. Inline mode leaves
// the mouse to the terminal and never gets here.

// selection is a drag over the fullscreen transcript, in content
// coordinates: line indexes into the viewport's content (transcript plus
// live tail) and cell columns on the screen.
type selection struct {
	anchorLine, anchorCol int
	endLine, endCol       int
	dragging              bool
}

// ordered returns the selection's start and end, start first.
func (s selection) ordered() (l0, c0, l1, c1 int) {
	if s.anchorLine < s.endLine || (s.anchorLine == s.endLine && s.anchorCol <= s.endCol) {
		return s.anchorLine, s.anchorCol, s.endLine, s.endCol
	}
	return s.endLine, s.endCol, s.anchorLine, s.anchorCol
}

// empty reports a click that did not drag.
func (s selection) empty() bool {
	return s.anchorLine == s.endLine && s.anchorCol == s.endCol
}

// span returns the cell range [from, to) the selection covers on line, and
// whether it covers any of it. The end cell is included.
func (s selection) span(line, width int) (from, to int, ok bool) {
	l0, c0, l1, c1 := s.ordered()
	if line < l0 || line > l1 {
		return 0, 0, false
	}
	from, to = 0, width
	if line == l0 {
		from = c0
	}
	if line == l1 {
		to = c1 + 1
	}
	return from, to, from < to
}

// selectionText is the plain text the selection covers in lines, with
// trailing blanks trimmed from each row and the left margin removed.
func selectionText(lines []string, s selection, width, margin int) string {
	l0, _, l1, _ := s.ordered()
	var out []string
	for i := l0; i <= l1 && i < len(lines); i++ {
		if i < 0 {
			continue
		}
		from, to, ok := s.span(i, width)
		if !ok {
			out = append(out, "")
			continue
		}
		if from < margin {
			from = margin
		}
		row := ""
		if from < to {
			row = ansi.Strip(ansi.Cut(lines[i], from, to))
		}
		out = append(out, strings.TrimRight(row, " "))
	}
	return strings.Trim(strings.Join(out, "\n"), "\n")
}

// highlightRow renders row with the cells [from, to) in reverse video.
func highlightRow(row string, from, to int) string {
	w := ansi.StringWidth(row)
	if to > w {
		row += strings.Repeat(" ", to-w)
		w = to
	}
	sel := ansi.Strip(ansi.Cut(row, from, to))
	return ansi.Cut(row, 0, from) + "\x1b[7m" + sel + "\x1b[27m" + ansi.Cut(row, to, w)
}

// msgCopied reports the outcome of copying a selection.
type msgCopied struct {
	chars int
	err   error
}

// copyToClipboard copies text through the terminal (OSC 52, which works
// over SSH when the terminal allows it) and through the local clipboard
// tool when there is one (iTerm2 refuses OSC 52 unless its "applications
// may access clipboard" option is on).
func copyToClipboard(text string) tea.Cmd {
	return tea.Batch(tea.SetClipboard(text), func() tea.Msg {
		chars := len([]rune(text))
		var cmd *exec.Cmd
		if custom := os.Getenv("HARNESS_CLIPBOARD_CMD"); custom != "" {
			// A shell command reading the text on stdin; tests use it to
			// avoid touching the real clipboard.
			cmd = exec.Command("/bin/sh", "-c", custom)
		}
		switch {
		case cmd != nil:
		case runtime.GOOS == "darwin":
			cmd = exec.Command("pbcopy")
		case runtime.GOOS == "linux":
			for _, c := range [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}} {
				if _, err := exec.LookPath(c[0]); err == nil {
					cmd = exec.Command(c[0], c[1:]...)
					break
				}
			}
		}
		if cmd == nil {
			return msgCopied{chars: chars} // OSC 52 only
		}
		cmd.Stdin = strings.NewReader(text)
		return msgCopied{chars: chars, err: cmd.Run()}
	})
}

// handleMouseSelect handles a left-button press, drag or release over the
// fullscreen transcript. It reports whether the event was a selection
// event at all.
func (m Model) handleMouseSelect(msg tea.Msg) (Model, tea.Cmd, bool) {
	if !m.fullscreen {
		return m, nil, false
	}
	vpHeight := m.viewport.Height()
	line := func(y int) int { return m.selectionTop() + y }
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft || msg.Y >= vpHeight {
			m.sel = nil
			return m, nil, msg.Button == tea.MouseLeft
		}
		m.sel = &selection{anchorLine: line(msg.Y), anchorCol: msg.X, endLine: line(msg.Y), endCol: msg.X, dragging: true}
		return m, nil, true
	case tea.MouseMotionMsg:
		if m.sel == nil || !m.sel.dragging {
			return m, nil, false
		}
		y := min(max(msg.Y, 0), vpHeight-1)
		m.sel.endLine, m.sel.endCol = line(y), max(msg.X, 0)
		return m, nil, true
	case tea.MouseReleaseMsg:
		if m.sel == nil || !m.sel.dragging {
			return m, nil, false
		}
		m.sel.dragging = false
		if m.sel.empty() {
			m.sel = nil
			return m, nil, true
		}
		text := selectionText(m.selectionContent(), *m.sel, m.width, m.margin())
		if text == "" {
			return m, nil, true
		}
		return m, copyToClipboard(text), true
	}
	return m, nil, false
}

// selectionContent is the viewport's content as displayed: the committed
// transcript followed by the live tail (fullscreenView folds them the same
// way).
func (m Model) selectionContent() []string {
	return append(append([]string{}, m.transcript...), m.liveTail(m.contentWidth())...)
}

// selectionTop is the content line shown on the viewport's first row,
// by fullscreenView's own rule: a viewport at the bottom follows the
// content (live tail included) down.
func (m Model) selectionTop() int {
	if m.viewport.AtBottom() {
		return max(len(m.transcript)+len(m.liveTail(m.contentWidth()))-m.viewport.Height(), 0)
	}
	return m.viewport.YOffset()
}

// copiedNote is the transient mode-line note after a copy.
func copiedNote(msg msgCopied) string {
	if msg.err != nil {
		return "copy failed: " + msg.err.Error()
	}
	if msg.chars == 1 {
		return "copied 1 character"
	}
	return fmt.Sprintf("copied %d characters", msg.chars)
}

// copiedNoteDuration is how long the copy note replaces the mode line.
const copiedNoteDuration = 2 * time.Second
