package tui

import (
	"strings"
	"testing"
)

func TestFormatTokens(t *testing.T) {
	cases := map[int]string{
		450:       "450",
		999:       "999",
		3400:      "3.4k",
		1_000_000: "1.0m",
		2_500_000: "2.5m",
	}
	for n, want := range cases {
		if got := FormatTokens(n); got != want {
			t.Errorf("FormatTokens(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestRenderSpinnerLeftSingleEllipsisWithTruncatedLabel is the regression
// for a real-session frame that showed a double ellipsis on a long bash
// command: "Running cd /private/tmp/-Us……  43s". RenderSpinnerLeft always
// appends its own trailing "…" to args.Label (the design's sim.status +
// '…'), so a label that already carries a truncation-marker "…" (as
// bridge.go's truncateBusyArg used to add) produced two in a row. The fix
// is in bridge.go's truncateBusyArg, which no longer appends one; this
// asserts the composed line still shows exactly one.
func TestRenderSpinnerLeftSingleEllipsisWithTruncatedLabel(t *testing.T) {
	label := "Running " + strings.Repeat("x", 30) // truncated, no trailing marker of its own
	line := stripANSI(RenderSpinnerLeft(SpinnerArgs{Frame: 0, Label: label, ElapsedSeconds: 43}))
	if strings.Count(line, "…") != 1 {
		t.Errorf("RenderSpinnerLeft produced %d ellipses, want exactly 1: %q", strings.Count(line, "…"), line)
	}
}

func TestPickLabelDeterministic(t *testing.T) {
	if PickLabel(0) != Labels[0] {
		t.Errorf("PickLabel(0) = %q, want %q", PickLabel(0), Labels[0])
	}
	if PickLabel(len(Labels)) != Labels[0] {
		t.Error("PickLabel should wrap around")
	}
}

func TestRenderToolCallMarkerColorByStatus(t *testing.T) {
	SetColorEnabled(true)
	defer SetColorEnabled(colorEnabled())

	ok := RenderToolCall(ToolCallView{Name: "Read", PrimaryArg: "f.go", Status: CallOK})[0]
	errLine := RenderToolCall(ToolCallView{Name: "Read", PrimaryArg: "f.go", Status: CallError})[0]
	running := RenderToolCall(ToolCallView{Name: "Read", PrimaryArg: "f.go", Status: CallRunning})[0]
	if ok == errLine || ok == running || errLine == running {
		t.Error("each status should render a distinctly colored marker")
	}
}

func TestRenderToolCallExpansionHint(t *testing.T) {
	view := ToolCallView{
		Name: "Read", PrimaryArg: "f.go", Status: CallOK,
		ResultLines: []string{"line one"}, TotalLines: 240, HasTotalLines: true,
	}
	out := RenderToolCall(view)
	// kiln's "tool" block adds a label-rule row above the head line, so
	// this is now label rule + head + result + hint.
	if len(out) != 4 {
		t.Fatalf("got %d lines, want label rule + head + result + hint", len(out))
	}
}

func TestRenderDiffLineNumbers(t *testing.T) {
	// kiln's "diff" block keeps context lines, dim, alongside the +/- rows
	// (docs/kiln-design-handoff/README.md's "diff" row: "context lines
	// dim") — unlike the pre-kiln Claude Code parity contract, which
	// dropped them.
	patch := "@@ -10,2 +10,3 @@\n-old line\n+new line one\n+new line two\n context line"
	out := RenderDiff(patch, 0)
	if len(out) != 4 {
		t.Fatalf("got %d lines, want 4 (1 removed + 2 added + 1 context)", len(out))
	}
}

func TestParseUnifiedDiffSkipsFileHeaders(t *testing.T) {
	// go-udiff's Unified() output (internal/tools/editdiff.go's
	// generateUnifiedPatch): "--- path\n+++ path\n@@ ... @@\n...". The
	// "--- "/"+++ " header lines must not be misread as -/+ content rows.
	patch := "--- math.js\n+++ math.js\n@@ -1,1 +1,1 @@\n-function add(a,b){ return a - b }\n+function add(a,b){ return a + b }\n"
	d := ParseUnifiedDiff(patch, 1)
	if d.Removed != 1 || d.Added != 1 {
		t.Fatalf("got Added=%d Removed=%d, want 1/1", d.Added, d.Removed)
	}
	if len(d.Lines) != 2 {
		t.Fatalf("got %d diff lines, want 2", len(d.Lines))
	}
	if d.Lines[0].Sign != '-' || d.Lines[0].Num != 1 {
		t.Errorf("got %+v, want removed line 1", d.Lines[0])
	}
	if d.Lines[1].Sign != '+' || d.Lines[1].Num != 1 {
		t.Errorf("got %+v, want added line 1", d.Lines[1])
	}
}

func TestRenderDiffLinesAlignsWidestNumber(t *testing.T) {
	lines := []DiffLine{{Num: 1, Sign: '-', Text: "a"}, {Num: 12, Sign: '+', Text: "b"}}
	out := RenderDiffLines(lines)
	// kiln's "edit" block anatomy: no leading indent (dropped from the
	// pre-kiln diffIndent) — number in a 4-column field (not padded to the
	// widest digit count — %4d always right-aligns within 4 columns) + " "
	// + a 2-column sign + the code, then padded to the full rule width by
	// the added/removed background tint.
	if got := stripANSI(out[0]); strings.TrimRight(got, " ") != "   1 − a" {
		t.Errorf("got %q, want %q", got, "   1 − a")
	}
	if got := stripANSI(out[1]); strings.TrimRight(got, " ") != "  12 + b" {
		t.Errorf("got %q, want %q", got, "  12 + b")
	}
}

func TestRenderToolCallDiffRendersHeaderAndCounts(t *testing.T) {
	view := ToolCallView{
		Name: "Update", PrimaryArg: "math.js", Status: CallOK,
		Diff: &ToolDiff{Added: 1, Removed: 1, Lines: []DiffLine{
			{Num: 1, Sign: '-', Text: "old"},
			{Num: 1, Sign: '+', Text: "new"},
		}},
	}
	out := RenderToolCall(view)
	// kiln's "edit" block: label rule ("edit" + filename meta) + the
	// panel header row (path, "+N", "−N") + 2 diff rows. There is no
	// separate "Update math.js" head line for a diff.
	if len(out) != 4 {
		t.Fatalf("got %d lines, want label rule + header + 2 diff rows: %v", len(out), out)
	}
	if !strings.Contains(stripANSI(out[1]), "+1") || !strings.Contains(stripANSI(out[1]), "−1") {
		t.Errorf("got %q, want the +1/−1 counts", stripANSI(out[1]))
	}
	if !strings.Contains(stripANSI(out[1]), "math.js") {
		t.Errorf("got %q, want the path on the header row", stripANSI(out[1]))
	}
}

func TestRenderToolCallDiffNewFileTag(t *testing.T) {
	view := ToolCallView{
		Name: "Write", PrimaryArg: "src/new.ts", Status: CallOK,
		Diff: &ToolDiff{Added: 1, NewFile: true, Lines: []DiffLine{
			{Num: 1, Sign: '+', Text: "export {}"},
		}},
	}
	out := RenderToolCall(view)
	if !strings.Contains(stripANSI(out[1]), "new file") {
		t.Errorf("header row %q missing the new file tag", stripANSI(out[1]))
	}
}

func TestMapToolNameKeepsEditAsEdit(t *testing.T) {
	if got := MapToolName("edit"); got != "Edit" {
		t.Errorf("MapToolName(edit) = %q, want Edit (the same name its diff block uses)", got)
	}
	if got := MapToolName("bash"); got != "Bash" {
		t.Errorf("MapToolName(bash) = %q, want Bash", got)
	}
}

// TestMapToolNameSnakeCaseTitleCased checks every snake_case tool name the
// registry actually has (internal/tools/*.go's Name fields) renders as a
// readable phrase instead of "Bash_background"/"Exit_plan_mode" (only the
// first rune upper-cased, underscore left in place).
// *qa/findings/20260927T000543Z-snake-case-tool-names-not-title-cased.json*.
func TestMapToolNameSnakeCaseTitleCased(t *testing.T) {
	cases := map[string]string{
		"bash":               "Bash",
		"read":               "Read",
		"write":              "Write",
		"task":               "Task",
		"bash_output":        "Bash output",
		"bash_background":    "Bash background",
		"kill_shell":         "Kill shell",
		"tool_search":        "Tool search",
		"exit_plan_mode":     "Exit plan mode",
		"session_search":     "Session search",
		"todo_write":         "Todo write",
		"mcp__fixture__echo": "Mcp fixture echo",
	}
	for name, want := range cases {
		if got := MapToolName(name); got != want {
			t.Errorf("MapToolName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestRenderToolGroupRowsCollapseAndPluralize(t *testing.T) {
	if got := stripANSI(RenderToolGroupRunning(GroupRead, 1)); got != "  Reading 1 file…" {
		t.Errorf("got %q", got)
	}
	if got := stripANSI(RenderToolGroupDone(GroupRead, 3)); got != "  Read 3 files" {
		t.Errorf("got %q", got)
	}
	if got := stripANSI(RenderToolGroupRunning(GroupBash, 1)); got != "⏺ Running 1 shell command…" {
		t.Errorf("got %q", got)
	}
	if got := stripANSI(RenderToolGroupDone(GroupBash, 2)); got != "  Ran 2 shell commands" {
		t.Errorf("got %q", got)
	}
}

// TestRenderAssistantTextHasLabelRuleThenPlainLines checks kiln's "text"
// block anatomy: a "kiln" label rule above the body, then the lines
// unchanged — no per-line "⏺" marker or continuation indent (that was
// Claude Code's turn-summary model, which kiln's label rules replace per
// docs/kiln-design.md).
func TestRenderAssistantTextHasLabelRuleThenPlainLines(t *testing.T) {
	out := RenderAssistantText([]string{"Done.", "more"})
	if len(out) != 3 {
		t.Fatalf("got %d lines, want label rule + 2 body lines", len(out))
	}
	if !strings.Contains(stripANSI(out[0]), "kiln") {
		t.Errorf("row 0 = %q, want the \"kiln\" label rule", stripANSI(out[0]))
	}
	if stripANSI(out[1]) != "Done." {
		t.Errorf("got %q, want %q", stripANSI(out[1]), "Done.")
	}
	if stripANSI(out[2]) != "more" {
		t.Errorf("got %q, want %q", stripANSI(out[2]), "more")
	}
}

// stripANSI removes SGR escapes so a rendered line can be compared against
// plain expected text regardless of whether colour is enabled.
func stripANSI(s string) string {
	var out strings.Builder
	inEsc := false
	for _, r := range s {
		if r == '\x1b' {
			inEsc = true
			continue
		}
		if inEsc {
			if r == 'm' {
				inEsc = false
			}
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

// TestRenderThinkingCollapsedVsExpanded checks the fix for defect
// 20260926T232657Z-thinking-invisible: a committed thinking block is a dim
// "thinking" label rule (docs/kiln-design-handoff/README.md "Block
// anatomy"), never nothing — the old version returned no lines at all
// unless Expanded was already true, and nothing ever set Expanded true on
// a committed block, so a thinking block never appeared in any state. ∴ is
// gone from the label rule (not in the design's glyph set); collapsed
// shows the rule plus a one-line summary and a "ctrl+o to expand" hint,
// expanded shows the rule plus the full text.
func TestRenderThinkingCollapsedVsExpanded(t *testing.T) {
	collapsed := RenderThinking(ThinkingView{Text: "reasoning here", Active: false, Expanded: false})
	if len(collapsed) != 2 {
		t.Fatalf("collapsed thinking should be label rule + one-line summary, got %d lines: %#v", len(collapsed), collapsed)
	}
	if !strings.HasPrefix(stripANSI(collapsed[0]), "thinking ") {
		t.Errorf("collapsed thinking's first line = %q, want a \"thinking ────\" label rule (no ∴ glyph)", stripANSI(collapsed[0]))
	}
	if !strings.Contains(stripANSI(collapsed[1]), "reasoning here") {
		t.Errorf("collapsed thinking's summary line = %q, want it to contain the reasoning text", stripANSI(collapsed[1]))
	}
	if strings.Contains(strings.Join(collapsed, "\n"), "∴") {
		t.Error("collapsed thinking must not use the ∴ glyph — not in the design's glyph set")
	}

	multiline := RenderThinking(ThinkingView{Text: "first line\nsecond line\nthird line", Expanded: false})
	if len(multiline) != 3 {
		t.Fatalf("collapsed multi-line thinking should be rule + summary + hint, got %d lines: %#v", len(multiline), multiline)
	}
	if !strings.Contains(stripANSI(multiline[2]), "ctrl+o to expand") {
		t.Errorf("collapsed multi-line thinking's last line = %q, want a \"ctrl+o to expand\" hint", stripANSI(multiline[2]))
	}

	expanded := RenderThinking(ThinkingView{Text: "reasoning here", Active: false, Expanded: true})
	if len(expanded) != 2 {
		t.Fatalf("expanded thinking should be rule + body, got %d", len(expanded))
	}
	if !strings.Contains(stripANSI(expanded[1]), "reasoning here") {
		t.Errorf("expanded thinking's body = %q, want the full reasoning text", stripANSI(expanded[1]))
	}

	empty := RenderThinking(ThinkingView{Text: "   "})
	if len(empty) != 0 {
		t.Error("blank thinking text should render nothing")
	}
}

func TestPrimaryArgPicksIdentifyingKey(t *testing.T) {
	cases := []struct {
		args any
		want string
	}{
		{"bare string", "bare string"},
		{map[string]any{"path": "a.go"}, "a.go"},
		{map[string]any{"command": "ls -la"}, "ls -la"},
		{map[string]any{"unrelated": 1}, ""},
	}
	for _, c := range cases {
		if got := PrimaryArg(c.args); got != c.want {
			t.Errorf("PrimaryArg(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}

// TestRenderToolCallFailedHintBeforeKeptTail: a clipped failed result keeps
// its last line (the exit status); the "… +N lines" hint stands where the
// hidden lines were, above it, not after it.
func TestRenderToolCallFailedHintBeforeKeptTail(t *testing.T) {
	lines := RenderToolCall(ToolCallView{
		Name: "Bash", PrimaryArg: "npm test", Status: CallError,
		ResultLines:   []string{"FAIL upload", "TypeError: boom", "Command exited with code 1"},
		TotalLines:    5,
		HasTotalLines: true,
	})
	var hint, tail = -1, -1
	for i, l := range lines {
		plain := stripANSI(l)
		if strings.Contains(plain, "… +2 lines") {
			hint = i
		}
		if strings.Contains(plain, "Command exited with code 1") {
			tail = i
		}
	}
	if hint < 0 || tail < 0 || hint != tail-1 {
		t.Errorf("hint at %d, exit line at %d; want the hint directly above the exit line:\n%s", hint, tail, strings.Join(lines, "\n"))
	}
}

func TestParseUnifiedDiffMarksGapBetweenHunks(t *testing.T) {
	patch := "@@ -2,1 +2,1 @@\n-a\n+b\n@@ -40,1 +40,1 @@\n-c\n+d\n"
	d := ParseUnifiedDiff(patch, 1)
	if len(d.Lines) != 5 || d.Lines[2].Sign != '~' {
		t.Fatalf("got %+v, want a '~' gap row between the two hunks", d.Lines)
	}
	if d.Added != 2 || d.Removed != 2 {
		t.Fatalf("gap row counted as a change: Added=%d Removed=%d", d.Added, d.Removed)
	}
	out := RenderDiffLines(d.Lines)
	if got := stripANSI(out[2]); !strings.Contains(got, "…") || strings.ContainsAny(got, "+−") {
		t.Errorf("gap row renders as %q, want a dim … row", got)
	}
}

// TestRenderToolCall_BlankOutputHasNoResultRow: a command whose output was
// only whitespace draws no "→" row with nothing after it.
func TestRenderToolCall_BlankOutputHasNoResultRow(t *testing.T) {
	view := ToolCallView{Name: "Bash", PrimaryArg: "go build ./...", Status: CallOK, ResultLines: []string{"", " "}, TotalLines: 2, HasTotalLines: true}
	for _, l := range RenderToolCall(view) {
		if strings.Contains(stripANSI(l), "→") {
			t.Errorf("blank output rendered a result row: %q", stripANSI(l))
		}
	}
}

// TestSkillBlockNamesTheSkillAndCollapsesItsText: a skill call shows the
// skill's name as its argument and "loaded · N lines" instead of the
// skill's instructions, except in verbose mode or on failure.
func TestSkillBlockNamesTheSkillAndCollapsesItsText(t *testing.T) {
	if got := PrimaryArg(map[string]any{"skill": "rubber-ducky:rubber-ducky"}); got != "rubber-ducky:rubber-ducky" {
		t.Errorf("PrimaryArg = %q, want the skill name", got)
	}
	text := []string{"Base directory for this skill: /x", "# Rubber Ducky", "Ask one question at a time."}
	if got := collapsedSummary("skill", text, false, false); len(got) != 1 || got[0] != "loaded · 3 lines" {
		t.Errorf("collapsed = %q", got)
	}
	if got := collapsedSummary("skill", text, false, true); len(got) != 3 {
		t.Errorf("verbose = %q, want the full text", got)
	}
	if got := collapsedSummary("skill", []string{"unknown skill"}, true, false); got[0] != "unknown skill" {
		t.Errorf("failed = %q, want the error", got)
	}
}
