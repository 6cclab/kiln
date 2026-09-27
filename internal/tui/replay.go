package tui

import (
	"path/filepath"
	"strings"

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
// transcript would have committed it at the given width and verbosity: user
// echoes, grouped or full tool calls with results, assistant text, plus
// every synthetic (non-entry) block recorded via Bridge.CommitSynthetic —
// the committed plan checklist, subagent dispatch lines, system notes and
// the /context block, none of which are session entries and so would
// otherwise be silently dropped by a replay built purely from the log.
// synthetics is spliced back in commit order, each one immediately after
// the entry it was tagged with (empty AfterEntryID means "before the
// first entry") — see SyntheticCommit's doc comment. It is what Ctrl+O,
// Ctrl+F and Rewind redraw the screen from.
func RenderTranscriptEntries(entries []session.Entry, width int, verbose bool, cwd string, synthetics []SyntheticCommit) []string {
	var out []string
	calls := map[string]msg.ToolCall{}
	renderer := NewMarkdownRenderer(width, IsPlain())

	bySynthetic := map[string][]SyntheticCommit{}
	for _, sc := range synthetics {
		bySynthetic[sc.AfterEntryID] = append(bySynthetic[sc.AfterEntryID], sc)
	}
	emitSynthetics := func(afterID string) {
		for _, sc := range bySynthetic[afterID] {
			out = append(out, sc.Lines...)
		}
	}

	emitSynthetics("")
	for _, e := range entries {
		if e.Type != session.EntryMessage || e.Message == nil {
			continue
		}
		switch m := e.Message.(type) {
		case msg.UserMessage:
			if text := textOf(m.Content); strings.TrimSpace(text) != "" {
				out = append(out, RenderUserMessage(text, width)...)
			}
		case msg.AssistantMessage:
			for _, c := range m.Content {
				switch cv := c.(type) {
				case msg.ToolCall:
					calls[cv.ID] = cv
				case msg.ThinkingContent:
					// A committed thinking block replays exactly like the
					// live one (app.go's handleThinking), just sourced from
					// the session log's AssistantMessage content instead of
					// streamed deltas — same RenderThinking, same
					// Expanded-by-verbosity rule, so ctrl+o toggles it the
					// same way it toggles a tool block's collapsed output
					// (defect 20260926T232657Z-thinking-invisible).
					view := ThinkingView{Text: cv.Thinking, Expanded: verbose}
					out = append(out, "")
					out = append(out, FitLines(RenderThinking(view), width, resultIndent)...)
				}
			}
			if text := strings.TrimSpace(textOf(m.Content)); text != "" {
				// A leading blank row, matching the live commit path
				// (app.go's msgCommitMarkdown: `append([]string{""},
				// RenderAssistantText(...)...)`) — this replay used to
				// omit it, the other half of defect
				// 20260926T232657Z-verbose-toggle-inconsistent's missing
				// blank row (the ToolResultMessage branch above had the
				// same gap; a plain user-message-then-text turn, with no
				// tool call in between, hits this branch directly instead).
				out = append(out, "")
				out = append(out, RenderAssistantText(renderer.Render(text))...)
				out = append(out, "")
			}
		case msg.ToolResultMessage:
			call := calls[m.ToolCallID]
			name := m.ToolName
			if name == "" {
				name = call.Name
			}
			// One renderer for a committed tool block, live or replayed: no
			// grouping here (the live commit path — app.go's flushGroup —
			// only ever groups the *in-flight* indicator row, never a
			// committed block; grouping a committed replay into a "Read N
			// files" summary, as this used to, was a third, inconsistent
			// rendering ctrl+o could land on — defect
			// 20260926T232657Z-verbose-toggle-inconsistent). A leading
			// blank row matches every other block and the live path's own
			// commit (app.go's msgCommitToolCall/flushGroup both prepend
			// one) — this replay used to omit it, dropping the blank row
			// before whatever followed.
			view := toolCallViewFor(name, call, &m, verbose, cwd)
			out = append(out, "")
			out = append(out, FitLines(RenderToolCall(view), width, "     ")...)
			out = append(out, "")
		}
		emitSynthetics(e.ID)
	}
	// The turn summary row is gone (kiln design: the busy line just
	// disappears at turn end, docs/kiln-design-handoff/README.md
	// "Interactions"); a Ctrl+O replay no longer re-appends one.
	return out
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
	// Read's result summarises the same way live does (bridge.go's
	// EventToolEnd handler, summarizeToolResult): the actual content
	// preview, clipped to the tier's line budget. A replay used to
	// override this to a bare "Read N lines" line count instead
	// (matching Claude Code's own verbose parity, not this design), which
	// meant ctrl+o showed *less* detail than the collapsed default and a
	// different rendering altogether from the one already committed live
	// — defect 20260926T232657Z-verbose-toggle-inconsistent.
	status := CallOK
	if result.IsError {
		status = CallError
	}
	view := ToolCallView{
		Name:          MapToolName(name),
		PrimaryArg:    primary,
		Status:        status,
		ResultLines:   clipResultLines(summary, max, status),
		TotalLines:    len(summary),
		HasTotalLines: true,
	}
	if strings.EqualFold(name, "edit") && view.Status == CallOK {
		if d := diffFromToolDetails(result.Details); d != nil {
			view.Diff = d
			view.ResultLines = nil
		}
	}
	return view
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
