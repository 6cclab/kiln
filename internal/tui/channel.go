package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// channelMetaOrder is the attribute order for framed channel events, so the
// framing is deterministic (map iteration is not). Only keys present and
// non-empty in the event's meta are rendered.
var channelMetaOrder = []string{"project", "request", "decision", "event", "status", "page", "spec", "heading"}

// frameChannelEvent renders a channel event as the <channel source="..."> turn
// the model receives, mirroring Claude Code: the source and the structured
// meta as attributes, the content as the body.
func frameChannelEvent(source, content string, meta map[string]any) string {
	var b strings.Builder
	b.WriteString(`<channel source="`)
	b.WriteString(source)
	b.WriteByte('"')
	for _, k := range channelMetaOrder {
		v, ok := meta[k]
		if !ok {
			continue
		}
		s, ok := v.(string)
		if !ok || s == "" {
			continue
		}
		fmt.Fprintf(&b, ` %s=%q`, k, s)
	}
	b.WriteString(">\n")
	b.WriteString(content)
	b.WriteString("\n</channel>")
	return b.String()
}

// injectChannelEvent delivers a server-pushed channel event into the session
// as a turn, routed exactly as a submitted prompt is: queued behind a waiting
// permission prompt or a background command, steered into a running turn, or
// started as a fresh turn when idle. Unlike handleSubmit it does no command
// or history handling — a channel push is always model-bound prose.
func (m Model) injectChannelEvent(ev MsgChannelEvent) (tea.Model, tea.Cmd) {
	framed := frameChannelEvent(ev.Source, ev.Content, ev.Meta)
	width := m.contentWidth()
	switch {
	case m.prompt.Active(), m.background != nil:
		// A prompt is waiting on an answer, or a background command is up
		// with no running turn to steer into: hold it until the lane drains
		// (same as handleSubmit's background branch).
		m.queued = append(m.queued, framed)
		m.spinner.SetQueueLen(len(m.queued))
		m.commit(RenderQueuedFollowUp(framed, width))
		return m, nil
	case m.busy:
		m.queued = append(m.queued, framed)
		if m.cfg.Lane != nil {
			if err := m.cfg.Lane.SteerAs(framed, framed); err != nil {
				m.commit(RenderError(err.Error()))
			}
		}
		m.commit(RenderQueuedFollowUp(framed, width))
		return m, nil
	default:
		m.commit(RenderUserMessage(framed, width))
		return m.beginTurn(framed, framed, nil)
	}
}
