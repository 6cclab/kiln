package tui

import (
	"fmt"
	"strconv"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/commands"
)

// dialogMCP is the Dialog implementation for /mcp
// (commands.ModalSpec.Kind == "mcp"): a sectioned server list (section
// headers from Item.Group) with a status-glyph gutter (Item.Marker), and
// Enter opens a per-server detail view (status line + Item.Tools, or the
// failure's Item.Error/Item.Detail).
//
// Row shape (title, count row, one section header, a connected row, a
// failed row, the legend) was originally verified against
// testdata/reference/claude-code/dialog-mcp.txt at terminal width 100
// with the harness's own values substituted (the harness has one flat
// server source, ~/.claude.json or --mcp-config, so only one "User MCPs
// (<path>)" section exists; no claude.ai/Built-in sections, no ⚠/◯ rows,
// since the harness has no auth-needed or disabled-server state — see
// internal/commands/manage_commands.go's mcpModal doc comment). The kiln
// restyle (QA findings 20260927T000712Z-dialog-chrome-effort-indicator
// and 20260927T000726Z-mcp-dialog-glyphs-plural) replaced that
// reference's ✔/✘/※ glyphs and "N servers" count with the design's own
// ✓/✕ (mcpStatusGlyph), a plain hint line, and pluralServers. See
// TestDialogMCP_MatchesReferenceStructure in dialog_mcp_test.go for the
// row-by-row diff.
type dialogMCP struct {
	spec     commands.ModalSpec
	cursor   int
	detail   bool
	status   string
	statusOK bool
}

// NewDialogMCP builds the /mcp Dialog. Called by
// internal/tui.NewCommandDialog when spec.Kind == "mcp" — see this file's
// package doc note in the handback report for the exact dispatch
// dialog.go (owned by another agent) needs to add.
func NewDialogMCP(spec commands.ModalSpec) Dialog {
	return &dialogMCP{spec: spec}
}

const dialogMCPTitle = "Manage MCP servers"

func (d *dialogMCP) HandleKey(msg tea.KeyPressMsg) (consumed, closeIt bool, cmd tea.Cmd) {
	key := msg.String()
	if d.detail {
		if key == "esc" {
			d.detail = false
			return true, false, nil
		}
		return true, false, nil
	}

	switch key {
	case "esc":
		return true, true, nil
	case "up", "k":
		if d.cursor > 0 {
			d.cursor--
		}
		return true, false, nil
	case "down", "j":
		if d.cursor < len(d.spec.Items)-1 {
			d.cursor++
		}
		return true, false, nil
	case "enter":
		if _, ok := d.selected(); ok {
			d.detail = true
		}
		return true, false, nil
	}
	return true, false, nil
}

func (d *dialogMCP) Apply(r msgDialogResult) {
	if r.err != nil {
		d.status, d.statusOK = r.err.Error(), false
		return
	}
	d.status, d.statusOK = r.msg, true
}

func (d *dialogMCP) selected() (commands.Item, bool) {
	if d.cursor < 0 || d.cursor >= len(d.spec.Items) {
		return commands.Item{}, false
	}
	return d.spec.Items[d.cursor], true
}

// FrameLabel names the label rule DialogTopRule draws above /mcp.
func (d *dialogMCP) FrameLabel() string { return "mcp" }

// pluralServers renders "N server"/"N servers" (QA finding
// 20260927T000726Z-mcp-dialog-glyphs-plural: the count row said "1
// servers").
func pluralServers(n int) string {
	if n == 1 {
		return "1 server"
	}
	return fmt.Sprintf("%d servers", n)
}

// renderMCPListRows lays out the sectioned server list: a two-space
// indented section header row whenever Item.Group changes (blank row
// before every header after the first), then per-item rows
// "<gutter><marker> <name>   <description>" — gutter is selectionGutter's
// blank-or-"> " two cells (dialog-mcp.txt rows 12-21 originally measured
// this at header dialogIndent+"  "+text, row
// dialogIndent+gutter+marker+" "+name), description column separated by
// three spaces, not a computed column — unlike /model's rows, /mcp's
// rows are NOT aligned to a common description column; each row's gap is
// fixed at three spaces after the name.
// mcpStatusGlyph colours a server row's status marker per kiln: green for
// connected, red for failed, faint otherwise. The rendered glyph always
// comes from the active glyph table (G().OK/G().Fail, ✓/✕ with ASCII
// "+"/"x" fallbacks in plain mode) rather than echoing back the
// commands.Item.Marker sentinel verbatim — that sentinel is still the
// literal "✔"/"✘" strings internal/commands uses to mean "connected"/
// "failed" (see internal/commands/manage_commands.go), but the design
// glyph is ✓/✕, not ✔/✘ (QA finding
// 20260927T000726Z-mcp-dialog-glyphs-plural).
func mcpStatusGlyph(marker string) string {
	switch marker {
	case "✔":
		return KilnGreen(G().OK)
	case "✘":
		return KilnRed(G().Fail)
	default:
		return Faint(marker)
	}
}

func renderMCPListRows(items []commands.Item, cursor, width int) []string {
	var out []string
	lastGroup := ""
	first := true
	for i, it := range items {
		if it.Group != "" && it.Group != lastGroup {
			if !first {
				out = append(out, "")
			}
			out = append(out, dialogIndent+"  "+Muted(it.Group))
			lastGroup = it.Group
		}
		first = false

		gutter := selectionGutter(i == cursor)
		name := Muted(it.Label)
		desc := Muted(it.Description)
		if i == cursor {
			name = KilnAmber(it.Label)
		}
		row := dialogIndent + gutter + mcpStatusGlyph(it.Marker) + " " + name
		if it.Description != "" {
			row += "   " + desc
		}
		if i == cursor {
			row = OnRaise(padTo(row, width))
		}
		out = append(out, row)
	}
	return out
}

func (d *dialogMCP) renderList(width, height int) []string {
	var out []string
	out = append(out, dialogIndent+KilnAmber(Bold(dialogMCPTitle)))
	out = append(out, dialogIndent+Muted(pluralServers(len(d.spec.Items))))
	out = append(out, "")
	out = append(out, renderMCPListRows(d.spec.Items, d.cursor, width)...)
	out = append(out, "")

	anyFailed := false
	for _, it := range d.spec.Items {
		if it.Marker == "✘" {
			anyFailed = true
			break
		}
	}
	if anyFailed {
		// Plain dim hint line, no leading glyph (QA finding
		// 20260927T000726Z-mcp-dialog-glyphs-plural flagged the "※" as
		// outside the design's glyph set; the fix is to drop it, not
		// swap in a different one).
		out = append(out, dialogIndent+Faint("Run kiln --debug to see error logs"))
	}
	out = append(out, dialogIndent+Faint("kiln doctor for details"))

	if d.status != "" {
		colour := KilnGreen
		if !d.statusOK {
			colour = KilnRed
		}
		out = append(out, dialogIndent+colour(d.status))
	}

	out = append(out, renderLegend([]string{
		"↑/↓ to navigate",
		"Enter to confirm",
		"Esc to cancel",
	}, width)...)

	if height > 0 && len(out) > height {
		out = out[:height]
	}
	return out
}

func (d *dialogMCP) renderDetail(width, height int) []string {
	item, ok := d.selected()
	var out []string
	if !ok {
		out = append(out, dialogIndent+Muted(Bold("(no server selected)")))
		out = append(out, renderLegend([]string{"Esc to back"}, width)...)
		return out
	}

	out = append(out, dialogIndent+KilnAmber(Bold(item.Label)))
	switch item.Marker {
	case "✔":
		status := fmt.Sprintf("connected · %d tools", len(item.Tools))
		if item.Ms > 0 {
			status += " · " + strconv.FormatInt(item.Ms, 10) + "ms"
		}
		out = append(out, dialogIndent+KilnGreen(status))
	default:
		reason := item.Error
		if reason == "" {
			reason = "unknown"
		}
		out = append(out, dialogIndent+KilnRed("failed: "+reason))
		if item.Detail != "" {
			wrapWidth := width - len(dialogIndent)
			if wrapWidth < 10 {
				wrapWidth = 10
			}
			for _, line := range wrapPlain(item.Detail, wrapWidth) {
				out = append(out, dialogIndent+Muted(line))
			}
		}
	}
	out = append(out, "")
	for _, t := range item.Tools {
		out = append(out, dialogIndent+Ink(t))
	}
	out = append(out, "")
	out = append(out, renderLegend([]string{"Esc to back"}, width)...)

	if height > 0 && len(out) > height {
		out = out[:height]
	}
	return out
}

func (d *dialogMCP) Render(width, height int) []string {
	if d.detail {
		return d.renderDetail(width, height)
	}
	return d.renderList(width, height)
}
