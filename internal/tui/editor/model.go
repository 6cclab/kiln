package editor

import (
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

// maxTextareaHeight caps how tall the editor grows before it starts
// scrolling internally, matching the plan's "max height e.g. 8, then
// internal scroll".
const maxTextareaHeight = 8

// EventKind reports what, if anything, an Update call produced.
type EventKind int

const (
	// EventNone means the keystroke was consumed (or ignored) with no
	// user-visible outcome beyond the editor's own state changing.
	EventNone EventKind = iota
	// EventSubmit means Enter submitted the buffer; Event.Text is the
	// submitted value (already trimmed of nothing — trimming, if wanted,
	// is the caller's job, matching pi-tui's own submitValue trimming
	// being visible to callers via the value it hands onSubmit).
	EventSubmit
	// EventCancel means Esc was pressed while no popup owned it.
	EventCancel
)

// Event is Update's second return value: what the keystroke did, beyond
// mutating the Model itself.
type Event struct {
	Kind EventKind
	Text string
}

// Model wraps bubbles/v2 textarea with the editing behaviour Claude Code's
// input line has that a bare textarea does not: Enter-submits-not-inserts,
// the `\`+Enter and Shift/Alt+Enter newline escapes, prompt history recalled
// at the buffer's edges, and a small kill ring. See doc.go for the full
// account of what textarea already provided.
type Model struct {
	ta     textarea.Model
	styles Styles

	// PopupActive is set by the app while an autocomplete popup (the `/`
	// or `@` list) is open. Tab, Enter and Esc belong to the popup then —
	// the editor must not submit, insert a newline or fire EventCancel for
	// them.
	PopupActive bool

	// pendingBackslash implements the two-state machine for `\` + Enter:
	// armed the instant a literal backslash is typed, disarmed by any
	// other keystroke (including a second backslash — only the backslash
	// immediately before Enter counts).
	pendingBackslash bool

	// history is oldest-first, newest-last — the same order Load returns
	// and Append writes.
	history []string
	// historyIdx is -1 when showing the live draft, otherwise an index
	// into history: the entry currently recalled.
	historyIdx int
	// draft holds the buffer as it was before the first Up press started
	// browsing, so Down past the newest entry can restore it (pi-tui's
	// "navigating past the newest entry returns to the draft").
	draft string

	kill killRing
}

// New builds an unfocused, empty Model. Call Focus (and issue its Cmd) once
// the editor should take input.
func New(styles Styles) Model {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.Placeholder = ""
	ta.EndOfBufferCharacter = ' '
	ta.MaxHeight = maxTextareaHeight
	ta.SetVirtualCursor(false) // hardware cursor via Cursor(), not an inline block
	ta.SetStyles(neutralTextareaStyles())
	ta.SetHeight(1)
	return Model{ta: ta, styles: styles, historyIdx: -1}
}

// neutralTextareaStyles strips textarea's own default background/foreground
// (DefaultDarkStyles paints a black background) so the editor renders in
// the terminal's own colours, matching Claude Code's plain input line —
// only the rules and marker (Styles.Rule) and the placeholder
// (Styles.Placeholder) carry colour.
func neutralTextareaStyles() textarea.Styles {
	var blank textarea.StyleState
	return textarea.Styles{Focused: blank, Blurred: blank}
}

// Focus focuses the editor and returns its cursor-blink command, if any.
func (m *Model) Focus() tea.Cmd { return m.ta.Focus() }

// Blur unfocuses the editor.
func (m *Model) Blur() { m.ta.Blur() }

// Focused reports whether the editor currently has focus.
func (m Model) Focused() bool { return m.ta.Focused() }

// Value returns the current buffer contents.
func (m Model) Value() string { return m.ta.Value() }

// SetValue replaces the buffer contents and exits history browsing (the
// new value becomes the draft).
func (m *Model) SetValue(s string) {
	m.ta.SetValue(s)
	m.exitHistoryBrowsing()
	m.syncHeight()
}

// Clear empties the buffer. The kill ring survives a clear — it is not
// part of the buffer's own state, the same way a shell's kill ring outlives
// a cleared command line.
func (m *Model) Clear() {
	m.ta.Reset()
	m.exitHistoryBrowsing()
	m.syncHeight()
}

// SetWidth configures the wrapped textarea's width, reserving the column(s)
// View spends on the marker prefix.
func (m *Model) SetWidth(width int) {
	m.ta.SetWidth(m.innerWidth(width))
}

// SetHistory replaces the recall list wholesale — used once at startup with
// Load's result. Entries are oldest-first, newest-last.
func (m *Model) SetHistory(entries []string) {
	m.history = append([]string(nil), entries...)
	m.historyIdx = -1
}

// AddToHistory appends one entry as the newest, e.g. right after a submit.
// Blank entries are ignored, matching Append's own semantics.
func (m *Model) AddToHistory(s string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	m.history = append(m.history, s)
	m.historyIdx = -1
}

// Cursor returns the hardware cursor position for the bordered frame
// View produces, offset for the marker-and-space prefix View adds to every
// content line and the top rule View adds above them. nil while blurred.
func (m Model) Cursor() *tea.Cursor {
	c := m.ta.Cursor()
	if c == nil {
		return nil
	}
	c.Position.X += m.markerColumns()
	c.Position.Y++ // the top rule occupies View's first line
	return c
}

// Update handles one message. tea.KeyPressMsg is routed through the
// submit/newline/history/kill-ring machinery below; tea.PasteMsg is handed
// to the textarea whole (bubbles/v2 already treats a paste as one atomic
// insert, so there is nothing left for this package to do); anything else
// (blink ticks, etc.) goes straight to the textarea.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd, Event) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.PasteMsg:
		m.pendingBackslash = false
		ta, cmd := m.ta.Update(msg)
		m.ta = ta
		m.historyIdx = -1
		m.syncHeight()
		return m, cmd, Event{}
	default:
		ta, cmd := m.ta.Update(msg)
		m.ta = ta
		return m, cmd, Event{}
	}
}

func (m Model) handleKey(msg tea.KeyPressMsg) (Model, tea.Cmd, Event) {
	s := msg.String()

	if m.PopupActive {
		switch s {
		case "tab", "enter", "esc":
			// The popup owns these; the app routes them to autocomplete.
			return m, nil, Event{}
		}
	}

	wasPendingBackslash := m.pendingBackslash
	m.pendingBackslash = false

	switch s {
	case "enter":
		if wasPendingBackslash {
			m.removeTrailingBackslash()
			m.ta.InsertString("\n")
			m.historyIdx = -1
			m.syncHeight()
			return m, nil, Event{}
		}
		text := m.ta.Value()
		m.ta.Reset()
		m.exitHistoryBrowsing()
		m.syncHeight()
		return m, nil, Event{Kind: EventSubmit, Text: text}

	case "shift+enter", "alt+enter":
		m.ta.InsertString("\n")
		m.historyIdx = -1
		m.syncHeight()
		return m, nil, Event{}

	case "esc":
		return m, nil, Event{Kind: EventCancel}

	case "up":
		if m.ta.Line() == 0 {
			m.navigateHistory(-1)
			return m, nil, Event{}
		}

	case "down":
		if m.ta.Line() == m.ta.LineCount()-1 {
			m.navigateHistory(1)
			return m, nil, Event{}
		}

	case "ctrl+k":
		m.killToLineEnd()
		return m, nil, Event{}

	case "ctrl+u":
		m.killToLineStart()
		return m, nil, Event{}

	case "ctrl+w", "alt+backspace":
		m.killWordBack()
		return m, nil, Event{}

	case "ctrl+y":
		m.yank()
		return m, nil, Event{}
	}

	before := m.ta.Value()
	ta, cmd := m.ta.Update(msg)
	m.ta = ta
	if m.ta.Value() != before {
		// Any edit adopts the currently-shown text (which may be a
		// recalled history entry) as the new draft, exactly like pi-tui's
		// exitHistoryBrowsing-on-edit.
		m.historyIdx = -1
		if msg.Text == "\\" {
			m.pendingBackslash = true
		}
	}
	m.syncHeight()
	return m, cmd, Event{}
}

// removeTrailingBackslash deletes the backslash that shouldSubmitOnBackslashEnter
// (app.ts / editor.js) would otherwise leave behind, via a synthetic
// backspace through the textarea rather than string surgery, so undo and
// cursor bookkeeping stay correct.
func (m *Model) removeTrailingBackslash() {
	ta, _ := m.ta.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	m.ta = ta
}

func (m *Model) exitHistoryBrowsing() {
	m.historyIdx = -1
	m.draft = ""
}

// navigateHistory implements pi-tui's navigateHistory (editor.js): dir -1
// is Up (older), dir 1 is Down (newer). The first Up recalls the newest
// entry and stashes the live buffer as the draft; Down past the newest
// entry restores that draft rather than continuing into "future" history
// that does not exist.
func (m *Model) navigateHistory(dir int) {
	if dir < 0 {
		if m.historyIdx == -1 {
			if len(m.history) == 0 {
				return
			}
			m.draft = m.ta.Value()
			m.historyIdx = len(m.history) - 1
			m.recall(m.history[m.historyIdx])
			return
		}
		if m.historyIdx > 0 {
			m.historyIdx--
			m.recall(m.history[m.historyIdx])
		}
		return
	}

	if m.historyIdx == -1 {
		return
	}
	if m.historyIdx < len(m.history)-1 {
		m.historyIdx++
		m.recall(m.history[m.historyIdx])
		return
	}
	draft := m.draft
	m.historyIdx = -1
	m.draft = ""
	m.recall(draft)
}

func (m *Model) recall(text string) {
	m.ta.SetValue(text)
	m.syncHeight()
}

// syncHeight grows the textarea with its content up to maxTextareaHeight,
// beyond which bubbles/v2's own viewport starts scrolling internally.
func (m *Model) syncHeight() {
	h := m.ta.LineCount()
	if h < 1 {
		h = 1
	}
	if h > maxTextareaHeight {
		h = maxTextareaHeight
	}
	if h != m.ta.Height() {
		m.ta.SetHeight(h)
	}
}

// currentLineRunes returns the cursor's line as runes and a column clamped
// to it, used by the kill-ring operations below to capture the text a
// forwarded textarea key is about to delete.
func (m Model) currentLineRunes() ([]rune, int, int) {
	lines := strings.Split(m.ta.Value(), "\n")
	row := m.ta.Line()
	if row < 0 {
		row = 0
	}
	if row >= len(lines) {
		row = len(lines) - 1
	}
	line := []rune(lines[row])
	col := m.ta.Column()
	if col < 0 {
		col = 0
	}
	if col > len(line) {
		col = len(line)
	}
	return line, row, col
}

func ctrlKey(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl}
}

// killToLineEnd is Ctrl+K: capture the text from the cursor to end of line
// (or, at end of line, the newline that a merge-with-next-line delete would
// consume) into the kill ring, then let the textarea's own DeleteAfterCursor
// binding (bound to ctrl+k by default) perform the deletion.
func (m *Model) killToLineEnd() {
	line, row, col := m.currentLineRunes()
	lineCount := m.ta.LineCount()
	if col >= len(line) {
		if row < lineCount-1 {
			m.kill.push("\n")
		}
	} else {
		m.kill.push(string(line[col:]))
	}
	ta, _ := m.ta.Update(ctrlKey('k'))
	m.ta = ta
	m.historyIdx = -1
	m.syncHeight()
}

// killToLineStart is Ctrl+U, the mirror of killToLineEnd.
func (m *Model) killToLineStart() {
	line, row, col := m.currentLineRunes()
	if col == 0 {
		if row > 0 {
			m.kill.push("\n")
		}
	} else {
		m.kill.push(string(line[:col]))
	}
	ta, _ := m.ta.Update(ctrlKey('u'))
	m.ta = ta
	m.historyIdx = -1
	m.syncHeight()
}

// killWordBack is Ctrl+W / Alt+Backspace: capture the word (and the
// whitespace deleteWordLeft is about to consume with it) before forwarding
// to the textarea's DeleteWordBackward binding.
func (m *Model) killWordBack() {
	line, row, col := m.currentLineRunes()
	if col == 0 {
		if row > 0 {
			m.kill.push("\n")
		}
	} else {
		start := wordBackStart(line, col)
		m.kill.push(string(line[start:col]))
	}
	ta, _ := m.ta.Update(tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModAlt})
	m.ta = ta
	m.historyIdx = -1
	m.syncHeight()
}

// yank is Ctrl+Y: insert the most recent kill without consuming it, so a
// second Ctrl+Y re-inserts the same text (see killring.go's doc comment on
// why there is no yank-pop cycle here).
func (m *Model) yank() {
	text, ok := m.kill.latest()
	if !ok {
		return
	}
	m.ta.InsertString(text)
	m.historyIdx = -1
	m.syncHeight()
}
