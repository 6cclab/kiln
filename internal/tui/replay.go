package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// groupKindFor reports which collapsed group a tool belongs to in the
// non-verbose transcript (docs/claude-code-reference.md §3: Read, Glob,
// Grep, Bash and web fetches group; Edit and Write never do). ok is false
// for tools that always render in full.
func groupKindFor(toolName string) (GroupKind, bool) {
	switch strings.ToLower(toolName) {
	case "read", "glob", "grep", "ls", "web_fetch", "webfetch", "web_search", "websearch", "tool_search":
		return GroupRead, true
	case "bash":
		return GroupBash, true
	}
	return "", false
}

// replayResultLines is how many result lines a replayed tool call shows in
// verbose mode before "… +N lines"; collapsed mode groups most calls away.
const replayResultLines = 50

// RenderTranscriptEntries renders a session branch the way the live
// transcript would have committed it at the given width and verbosity:
// user echoes, grouped or full tool calls with results, assistant text. It
// is what Ctrl+O and Rewind redraw the screen from.
func RenderTranscriptEntries(entries []session.Entry, width int, verbose bool, cwd string, summary []string) []string {
	var out []string
	calls := map[string]msg.ToolCall{}
	renderer := NewMarkdownRenderer(width, IsPlain())
	// toolsSinceText is set once a tool result rendered since the last
	// assistant text; verbose mode then places the right-aligned
	// "HH:MM AM model" row before that text (verbose-ctrl-o.txt row 22).
	toolsSinceText := false

	var groupKind GroupKind
	groupN := 0
	flush := func() {
		if groupN > 0 {
			out = append(out, RenderToolGroupDone(groupKind, groupN), "")
		}
		groupN = 0
	}

	for _, e := range entries {
		if e.Type != session.EntryMessage || e.Message == nil {
			continue
		}
		switch m := e.Message.(type) {
		case msg.UserMessage:
			flush()
			text := textOf(m.Content)
			if strings.TrimSpace(text) == "" {
				continue
			}
			out = append(out, RenderUserMessage(text, width)...)
		case msg.AssistantMessage:
			for _, c := range m.Content {
				if tc, ok := c.(msg.ToolCall); ok {
					calls[tc.ID] = tc
				}
			}
			if text := strings.TrimSpace(textOf(m.Content)); text != "" {
				flush()
				if verbose && toolsSinceText {
					out = append(out, RenderVerboseModelRow(time.UnixMilli(m.Timestamp), modelLabel(m), width))
				}
				toolsSinceText = false
				out = append(out, RenderAssistantText(renderer.Render(text))...)
				out = append(out, "")
			}
		case msg.ToolResultMessage:
			call := calls[m.ToolCallID]
			name := m.ToolName
			if name == "" {
				name = call.Name
			}
			if kind, grouped := groupKindFor(name); grouped && !verbose {
				if groupN > 0 && kind != groupKind {
					flush()
				}
				groupKind = kind
				groupN++
				continue
			}
			flush()
			toolsSinceText = true
			view := toolCallViewFor(name, call, &m, verbose, cwd)
			out = append(out, FitLines(RenderToolCall(view), width, "     ")...)
			out = append(out, "")
		}
	}
	flush()
	// The turn summary ("✻ Crunched for 4s · done …") is derived at turn
	// end, not stored as an entry, so a Ctrl+O replay re-appends the last
	// one the app kept (verbose-ctrl-o.txt row 24) rather than losing it.
	out = append(out, summary...)
	return out
}

// modelLabel is the provider/model id for the verbose model row, matching
// the footer's own display; the message's Model alone omits the provider.
func modelLabel(m msg.AssistantMessage) string {
	if m.Provider != "" {
		return m.Provider + "/" + m.Model
	}
	return m.Model
}

// toolCallViewFor builds the ToolCallView for a replayed call, sharing the
// bridge's own result summarising and Edit diff extraction.
func toolCallViewFor(name string, call msg.ToolCall, result *msg.ToolResultMessage, verbose bool, cwd string) ToolCallView {
	summary := summarizeToolResult(result)
	max := 3
	if verbose {
		max = replayResultLines
	}
	primary := PrimaryArg(call.Arguments)
	// Paths are relative in the collapsed view, absolute in verbose
	// (docs/claude-code-reference.md §3).
	if verbose && primary != "" && !filepath.IsAbs(primary) && isPathTool(name) {
		primary = filepath.Join(cwd, primary)
	}
	// Read's result is summarised as its line count ("Read 2 lines",
	// verbose-ctrl-o.txt row 14), not the file's content. Claude counts a
	// trailing newline as its own line, matching a text editor's line
	// count, so the raw read output's newline count plus one is used.
	if strings.EqualFold(name, "read") && !result.IsError {
		n := readLineCount(result)
		summary = []string{fmt.Sprintf("Read %d %s", n, plural(n, "line", "lines"))}
	}
	view := ToolCallView{
		Name:          MapToolName(name),
		PrimaryArg:    primary,
		Status:        CallOK,
		ResultLines:   clipTo(summary, max),
		TotalLines:    len(summary),
		HasTotalLines: true,
	}
	if result.IsError {
		view.Status = CallError
	}
	if strings.EqualFold(name, "edit") && view.Status == CallOK {
		if d := diffFromToolDetails(result.Details); d != nil {
			view.Diff = d
			view.ResultLines = nil
		}
	}
	return view
}

// readLineCount counts the lines in a read tool's raw output the way a
// text editor does: the number of "\n" plus one when there is any content
// (so "a\n" is two lines: "a" and the empty line after it).
func readLineCount(result *msg.ToolResultMessage) int {
	text := rawContent(result.Content)
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

// rawContent joins a tool result's text blocks verbatim, without
// summarizeLines' trailing-empty-line drop, so a trailing newline still
// counts toward the read's line count.
func rawContent(blocks msg.Blocks) string {
	var b strings.Builder
	for _, c := range blocks {
		if tc, ok := c.(msg.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// isPathTool reports whether a tool's primary argument is a file path.
func isPathTool(name string) bool {
	switch strings.ToLower(name) {
	case "read", "edit", "write", "glob", "grep", "ls":
		return true
	}
	return false
}

// textOf joins a message's text blocks.
func textOf(blocks msg.Blocks) string {
	var b strings.Builder
	for _, c := range blocks {
		if tc, ok := c.(msg.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
