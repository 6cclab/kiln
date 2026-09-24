package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// TranscriptView is the Ctrl+R alt-screen full-transcript view: a scroll
// offset over every entry in the current branch, rendered with tool
// results and thinking blocks expanded rather than collapsed.
//
// Scope note (see the phase report): this is a working but reduced
// implementation. It renders every entry with the existing pure
// renderers, expanded, and scrolls with the arrow keys/PageUp/PageDown,
// but it is not a bubbles/v2 viewport component (no mouse wheel, no
// search) and it does not diff against a live-updating branch while open
// — Bridge's alt-screen queue (see bridge.go's SetAltScreen) still
// guarantees no committed block is lost while it is open, it is just not
// reflected on screen until the view is rebuilt on next open.
type TranscriptView struct {
	lines  []string
	offset int
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
	m.transcriptView = &TranscriptView{lines: lines}
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

func (m Model) handleTranscriptViewKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	tv := m.transcriptView
	switch msg.String() {
	case "esc", "ctrl+r", "q":
		return m.closeTranscriptView(), nil
	case "up", "k":
		if tv.offset > 0 {
			tv.offset--
		}
	case "down", "j":
		if tv.offset < len(tv.lines)-1 {
			tv.offset++
		}
	case "pgup":
		tv.offset -= m.height
		if tv.offset < 0 {
			tv.offset = 0
		}
	case "pgdown":
		tv.offset += m.height
		if tv.offset > len(tv.lines)-1 {
			tv.offset = len(tv.lines) - 1
		}
		if tv.offset < 0 {
			tv.offset = 0
		}
	}
	return m, nil
}

func (m Model) transcriptViewView() tea.View {
	tv := m.transcriptView
	height := m.height
	if height <= 0 {
		height = 24
	}
	// Leave the last row for a footer hint.
	visible := height - 1
	if visible < 1 {
		visible = 1
	}
	end := tv.offset + visible
	if end > len(tv.lines) {
		end = len(tv.lines)
	}
	body := tv.lines[tv.offset:end]
	body = append(append([]string{}, body...), Dim("esc to return · ↑↓ scroll"))
	v := tea.NewView(strings.Join(body, "\n"))
	v.AltScreen = true
	return v
}
