package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/commands"
)

// referenceMCPItems mirrors testdata/reference/claude-code/dialog-mcp.txt
// rows 9-21's shape (title, count, one section, a ✔ row with a tool
// count, a ✘ row with none) using two of the harness's own MCP servers
// (argocd-mcp, proxmox — both real entries in this repo's ~/.claude.json,
// see the dialog-mcp.txt capture itself) rather than Claude Code's own
// fixture names, since the harness has no fixture data of its own.
func referenceMCPItems() []commands.Item {
	group := "User MCPs (/Users/andrepato/.claude.json)"
	return []commands.Item{
		{Value: "argocd-mcp", Label: "argocd-mcp", Group: group, Marker: "\u2714", Description: "16 tools", Tools: []string{"list_applications", "get_application"}, Ms: 420},
		{Value: "proxmox", Label: "proxmox", Group: group, Marker: "\u2718", Error: "command not found: tsx", Detail: "ENOENT: no such file or directory, posix_spawn '/Users/andrepato/projects/proxmox-mcp/node_modules/.bin/tsx'"},
	}
}

// TestDialogMCP_MatchesReferenceStructure diffs the harness's /mcp list
// view against dialog-mcp.txt's title, count row, section header, one ✔
// row, one ✘ row, the ※ row and the legend (width 100). The harness has
// no claude.ai/Built-in sections and no ⚠/◯ states (see mcpModal's doc
// comment in internal/commands/manage_commands.go for why), so those
// reference rows have no harness counterpart to test against.
func TestDialogMCP_MatchesReferenceStructure(t *testing.T) {
	SetColorEnabled(false)
	defer SetColorEnabled(true)

	spec := commands.ModalSpec{Items: referenceMCPItems()}
	d := NewDialogMCP(spec).(*dialogMCP)
	d.cursor = 0

	got := d.Render(100, 40)

	want := []string{
		"   Manage MCP servers",
		"   2 servers",
		"",
		"     User MCPs (/Users/andrepato/.claude.json)",
		"   \u276f \u2714 argocd-mcp   16 tools",
		"     \u2718 proxmox",
		"",
		"   \u203b Run kiln --debug to see error logs",
		"   kiln doctor for details",
		"   \u2191/\u2193 to navigate \u00b7 Enter to confirm \u00b7 Esc to cancel",
	}

	if len(got) != len(want) {
		t.Fatalf("row count = %d, want %d\ngot:  %q\nwant: %q", len(got), len(want), got, want)
	}
	for i := range want {
		row := got[i]
		// The selected row (the "❯" list item) is padded to the full
		// width by its raised background (OnRaise(padTo(...))); trim that
		// padding before comparing so this test only pins content, not
		// background width, which TestDialogMCP_Width60 already covers.
		if i == 4 {
			row = strings.TrimRight(row, " ")
		}
		if row != want[i] {
			t.Errorf("row %d:\n got:  %q\n want: %q", i, row, want[i])
		}
	}
}

// TestDialogMCP_Width60 checks the list reflows within a narrower width
// without a reference capture to diff against (none exists at 60 cols).
func TestDialogMCP_Width60(t *testing.T) {
	SetColorEnabled(false)
	defer SetColorEnabled(true)

	spec := commands.ModalSpec{Items: referenceMCPItems()}
	d := NewDialogMCP(spec).(*dialogMCP)

	got := d.Render(60, 40)
	for _, row := range got {
		if VisibleWidth(row) > 60 {
			t.Errorf("row exceeds width 60: %q (%d cols)", row, VisibleWidth(row))
		}
	}
}

// TestDialogMCP_NoFailuresOmitsDebugRow checks the ※ row only appears
// when a server actually failed, matching the reference's own
// conditional (dialog-mcp.txt shows it because proxmox is down; a
// capture with everything connected would not have it — inferred from
// the row's wording, "Run ... to see error logs", which implies errors
// exist).
func TestDialogMCP_NoFailuresOmitsDebugRow(t *testing.T) {
	SetColorEnabled(false)
	defer SetColorEnabled(true)

	spec := commands.ModalSpec{Items: []commands.Item{
		{Value: "ok", Label: "ok-server", Marker: "\u2714", Description: "1 tools"},
	}}
	d := NewDialogMCP(spec).(*dialogMCP)
	got := d.Render(100, 40)
	for _, row := range got {
		if row == "   \u203b Run harness --debug to see error logs" {
			t.Errorf("did not expect the ※ row with no failed servers, got %q", got)
		}
	}
}

// TestDialogMCP_EnterOpensDetail_EscGoesBack exercises the detail view:
// Enter on a connected server shows "connected · N tools · Nms" and its
// tool names; Esc returns to the list rather than closing the dialog
// (only Esc from the list itself closes it).
func TestDialogMCP_EnterOpensDetail_EscGoesBack(t *testing.T) {
	SetColorEnabled(false)
	defer SetColorEnabled(true)

	spec := commands.ModalSpec{Items: referenceMCPItems()}
	d := NewDialogMCP(spec).(*dialogMCP)
	d.cursor = 0

	consumed, closeIt, _ := d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !consumed || closeIt {
		t.Fatalf("enter: consumed=%v closeIt=%v, want consumed and not closed", consumed, closeIt)
	}
	if !d.detail {
		t.Fatal("expected detail mode after Enter")
	}

	rows := d.Render(100, 40)
	joined := ""
	for _, r := range rows {
		joined += r + "\n"
	}
	for _, want := range []string{"argocd-mcp", "connected \u00b7 2 tools \u00b7 420ms", "list_applications", "get_application", "Esc to back"} {
		if !strings.Contains(joined, want) {
			t.Errorf("detail view missing %q, got:\n%s", want, joined)
		}
	}

	consumed, closeIt, _ = d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !consumed || closeIt {
		t.Fatalf("esc from detail: consumed=%v closeIt=%v, want consumed and not closed", consumed, closeIt)
	}
	if d.detail {
		t.Fatal("expected list mode after Esc from detail")
	}

	consumed, closeIt, _ = d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !consumed || !closeIt {
		t.Fatalf("esc from list: consumed=%v closeIt=%v, want closeIt", consumed, closeIt)
	}
}

// TestDialogMCP_FailedServerDetail checks a failed server's detail view
// shows "failed: <Error>" plus the dim wrapped <Detail>.
func TestDialogMCP_FailedServerDetail(t *testing.T) {
	SetColorEnabled(false)
	defer SetColorEnabled(true)

	spec := commands.ModalSpec{Items: referenceMCPItems()}
	d := NewDialogMCP(spec).(*dialogMCP)
	d.cursor = 1
	d.detail = true

	rows := d.Render(100, 40)
	joined := ""
	for _, r := range rows {
		joined += r + "\n"
	}
	if !strings.Contains(joined, "failed: command not found: tsx") {
		t.Errorf("expected the failure reason, got:\n%s", joined)
	}
	if !strings.Contains(joined, "ENOENT") {
		t.Errorf("expected the wrapped detail text, got:\n%s", joined)
	}
}
