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
// Rows verified against testdata/reference/claude-code/dialog-mcp.txt
// (title, count row, one section header, one ✔ row, one ✘ row, the ※
// row, the legend — terminal width 100) with the harness's own values
// substituted (the harness has one flat server source, ~/.claude.json or
// --mcp-config, so only one "User MCPs (<path>)" section exists; no
// claude.ai/Built-in sections, no ⚠/◯ rows, since the harness has no
// auth-needed or disabled-server state — see
// internal/commands/manage_commands.go's mcpModal doc comment). See
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

// renderMCPListRows lays out the sectioned server list: a five-space
// indented section header row whenever Item.Group changes (blank row
// before every header after the first), then per-item rows
// "<gutter><marker> <name>   <description>" — gutter is "  " or "❯ ",
// matching dialog-mcp.txt rows 12-21 exactly (measured: header at
// dialogIndent+"  "+text, row at dialogIndent+gutter+marker+" "+name,
// description column separated by three spaces, not a computed column —
// unlike /model's rows, /mcp's reference rows are NOT aligned to a common
// description column; each row's gap is fixed at three spaces after the
// name).
// mcpStatusGlyph colours a server row's status marker per kiln: green for
// connected, red for failed, faint otherwise.
func mcpStatusGlyph(marker string) string {
	switch marker {
	case "✔":
		return KilnGreen(marker)
	case "✘":
		return KilnRed(marker)
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

		gutter := Faint("  ")
		name := Muted(it.Label)
		desc := it.Description
		if i == cursor {
			gutter = KilnAmber("❯ ")
			name = Ink(it.Label)
		}
		row := dialogIndent + gutter + mcpStatusGlyph(it.Marker) + " " + name
		if desc != "" {
			if i == cursor {
				desc = Ink(desc)
			} else {
				desc = Muted(desc)
			}
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
	out = append(out, dialogIndent+Muted(fmt.Sprintf("%d servers", len(d.spec.Items))))
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
		out = append(out, dialogIndent+Faint("※ Run kiln --debug to see error logs"))
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
