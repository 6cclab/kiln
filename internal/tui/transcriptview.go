package tui

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// msgAltScreenAppend is sent by Bridge (see bridge.go's run loop) for
// every block committed while the alt-screen transcript view is open, so
// Update can mirror it into the live view via TranscriptView.Append.
type msgAltScreenAppend struct {
	Text string
}

// TranscriptView is the Ctrl+R alt-screen full-transcript view: every
// entry in the current branch, rendered with tool results and thinking
// blocks expanded rather than collapsed, scrolled with a real
// bubbles/v2 viewport — so PageUp/PageDown, j/k/arrows and the mouse
// wheel all work the way they do everywhere else this dependency is
// already used (the editor's own textarea).
//
// Staying current while open: tea.Program.Println (what Bridge.run uses
// to print to scrollback) drops its output outright while the alt screen
// is active, so Bridge holds anything committed during that time and
// flushes it once the view closes (see bridge.go's SetAltScreen/held).
// That guarantees nothing is lost, but on its own it means the open view
// goes stale the instant a turn produces output. To keep it live, Bridge
// also sends a msgAltScreenAppend for each held commit (see bridge.go's
// run loop), which Update below appends straight into the viewport's
// content — the same text that will eventually print to scrollback,
// simply mirrored into the open view immediately instead of waiting for
// it to close. This is the simplest correct approach available without a
// second, richer renderer: the view does not re-run buildTranscriptLines
// against the growing branch, it just grows the same flat string the
// viewport already holds.
type TranscriptView struct {
	vp   viewport.Model
	text string
}

// buildTranscriptLines renders every entry in entries (oldest-first) with
// everything expanded.
func buildTranscriptLines(entries []session.Entry) []string {
	var out []string
	// Tool calls keyed by id, so each toolResult can be labelled with the
	// call's name and primary argument rather than a bare "Result".
	calls := map[string]msg.ToolCall{}
	for _, e := range entries {
		switch m := e.Message.(type) {
		case msg.UserMessage:
			out = append(out, RenderUserMessage(textOfBlocks(m.Content), 1000)...)
			out = append(out, "")
		case msg.AssistantMessage:
			for _, block := range m.Content {
				switch b := block.(type) {
				case msg.ThinkingContent:
					out = append(out, RenderThinking(ThinkingView{Text: b.Thinking, Expanded: true})...)
				case msg.TextContent:
					if strings.TrimSpace(b.Text) != "" {
						out = append(out, strings.Split(b.Text, "\n")...)
					}
				case msg.ToolCall:
					calls[b.ID] = b
				}
			}
			out = append(out, "")
		case msg.ToolResultMessage:
			status := CallOK
			if m.IsError {
				status = CallError
			}
			summary := textOfBlocks(m.Content)
			var lines []string
			if summary != "" {
				lines = strings.Split(summary, "\n")
			}
			name := titleCase(m.ToolName)
			var primary string
			if call, ok := calls[m.ToolCallID]; ok {
				if name == "" {
					name = titleCase(call.Name)
				}
				primary = PrimaryArg(call.Arguments)
			}
			if name == "" {
				name = "Result"
			}
			out = append(out, RenderToolCall(ToolCallView{Name: name, PrimaryArg: primary, Status: status, ResultLines: lines})...)
			out = append(out, "")
		}
	}
	if len(out) == 0 {
		out = []string{Dim("(nothing to show yet)")}
	}
	return out
}

func textOfBlocks(blocks msg.Blocks) string {
	var parts []string
	for _, b := range blocks {
		if t, ok := b.(msg.TextContent); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// openTranscriptView loads the branch's entries (oldest-first — FindEntries
// returns newestFirst) and enters the alt-screen view. If loading fails or
// there is no lane, the view still opens, showing the error/emptiness
// rather than silently doing nothing (a dead Ctrl+R reads as a broken
// key).
func (m Model) openTranscriptView() Model {
	var lines []string
	if m.cfg.Lane != nil {
		entries, err := m.cfg.Lane.FindEntries(context.Background())
		if err != nil {
			lines = []string{Red("transcript: " + err.Error())}
		} else {
			reversed := make([]session.Entry, len(entries))
			for i, e := range entries {
				reversed[len(entries)-1-i] = e
			}
			lines = buildTranscriptLines(reversed)
		}
	} else {
		lines = []string{Dim("(no session)")}
	}
	text := strings.Join(lines, "\n")
	vp := viewport.New(viewport.WithWidth(m.contentWidth()), viewport.WithHeight(m.transcriptViewportHeight()))
	vp.MouseWheelEnabled = true
	vp.SetContent(text)
	m.transcriptView = &TranscriptView{vp: vp, text: text}
	if m.cfg.Bridge != nil {
		m.cfg.Bridge.SetAltScreen(true)
	}
	return m
}

func (m Model) closeTranscriptView() Model {
	m.transcriptView = nil
	if m.cfg.Bridge != nil {
		m.cfg.Bridge.SetAltScreen(false)
	}
	return m
}

// transcriptViewportHeight is the viewport's own row count: the terminal
// height minus one row reserved for the "esc to return" hint, matching
// the old fixed layout's own reservation.
func (m Model) transcriptViewportHeight() int {
	height := m.height
	if height <= 0 {
		height = 24
	}
	if height < 2 {
		return 1
	}
	return height - 1
}

// Append grows the open view with one more block of already-rendered
// transcript text — see the TranscriptView doc comment on why this is
// how live updates work while the view is open. It preserves the read
// position: if the viewport was already scrolled to the bottom, it stays
// pinned there as new content arrives (like a live tail); otherwise the
// user's scroll position is left alone.
func (tv *TranscriptView) Append(text string) {
	pinned := tv.vp.AtBottom()
	if tv.text == "" {
		tv.text = text
	} else {
		tv.text += "\n" + text
	}
	tv.vp.SetContent(tv.text)
	if pinned {
		tv.vp.GotoBottom()
	}
}

func (m Model) handleTranscriptViewKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+r", "q":
		return m.closeTranscriptView(), nil
	}
	var cmd tea.Cmd
	m.transcriptView.vp, cmd = m.transcriptView.vp.Update(msg)
	return m, cmd
}

// handleTranscriptViewMouse forwards a mouse message (wheel scroll) to
// the viewport while the transcript view is open.
func (m Model) handleTranscriptViewMouse(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.transcriptView.vp, cmd = m.transcriptView.vp.Update(msg)
	return m, cmd
}

func (m Model) transcriptViewView() tea.View {
	tv := m.transcriptView
	tv.vp.SetWidth(m.contentWidth())
	tv.vp.SetHeight(m.transcriptViewportHeight())
	body := tv.vp.View() + "\n" + Dim("esc to return · ↑↓/pgup/pgdn/mouse wheel scroll")
	v := tea.NewView(body)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}
