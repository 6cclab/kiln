package tui

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Transcript rendering — the layout defined in docs/claude-code-reference.md
// §3, matched row-for-row against testdata/reference/claude-code/*.txt.
//
// Pure functions from data to lines. Nothing here touches a terminal, which
// is what lets the layout be asserted in tests rather than eyeballed, and
// is where parity is actually won or lost.

// Two spaces, glyph, two spaces -> content starts at column 5.
const resultIndent = "  "

// continuationIndent is the 2-column indent of tool-output lines after the
// "→ " first line (design "tool" row).
const continuationIndent = "  "

// fallbackRuleWidth is the label-rule width used by Render* functions that
// have no width parameter of their own (RenderToolCall, RenderTodos,
// RenderThinking, RenderAssistantText, RenderDiffLines,
// RenderToolGroupRunning/Done). Their signatures are load-bearing — app.go
// and bridge.go call them without a width — so this package cannot ask the
// caller's terminal width without changing every call site. A fixed
// fallback is the compromise: the header rule is drawn at a plausible
// width and the *content* below it still gets wrapped/fitted to the real
// terminal width downstream, by the caller, via FitLines(...,
// m.contentWidth(), ...) (see app.go:442) — only the rule's own fill length
// is approximate for these blocks. RenderUserMessage and
// RenderVerboseModelRow, which already take width, use it for real.
const fallbackRuleWidth = 56

// renderWidth is the live terminal content width, set by the app on every
// WindowSizeMsg (SetRenderWidth). The width-less Render* functions size
// their label rules and row-background tints to it so kiln's hairlines
// fill the full width. It falls back to fallbackRuleWidth before the first
// size message (e.g. in unit tests that call a renderer directly).
var renderWidth int

// SetRenderWidth records the current terminal content width for the
// width-less Render* helpers. A non-positive value is ignored.
func SetRenderWidth(w int) {
	if w > 0 {
		renderWidth = w
	}
}

// ruleWidth is the width the width-less blocks draw their label rule and
// full-row background tints at.
func ruleWidth() int {
	if renderWidth > 0 {
		return renderWidth
	}
	return fallbackRuleWidth
}

// longestLineWidth returns the widest visible line in lines, or 0.
func longestLineWidth(lines []string) int {
	max := 0
	for _, l := range lines {
		if w := VisibleWidth(l); w > max {
			max = w
		}
	}
	return max
}

// ruleWidthFor picks a label-rule width for a block with no width
// parameter: fallbackRuleWidth, or the widest body line, whichever is
// larger, so the rule never reads as narrower than its own content.
func ruleWidthFor(lines []string) int {
	if w := longestLineWidth(lines); w > ruleWidth() {
		return w
	}
	return ruleWidth()
}

// padToWidth right-pads an already-styled (possibly ANSI-coloured) string
// with spaces so a background tint applied around it spans the full width.
func padToWidth(styled string, width int) string {
	if pad := width - VisibleWidth(styled); pad > 0 {
		return styled + strings.Repeat(" ", pad)
	}
	return styled
}

// CallStatus is the state of a tool call, for marker colour.
type CallStatus string

const (
	CallRunning CallStatus = "running"
	CallOK      CallStatus = "ok"
	CallError   CallStatus = "error"
)

// DiffLine is one numbered row of an Edit's rendered diff.
type DiffLine struct {
	Num  int
	Sign byte // '-', '+', or ' ' for an unchanged context line
	Text string
}

// ToolDiff is the structured, already-computed diff for an Edit call's
// result row: "Added N lines, removed M lines" followed by the numbered
// rows. Built by ParseUnifiedDiff from a unified patch, or directly by a
// caller that already has old/new line pairs.
type ToolDiff struct {
	Added   int
	Removed int
	Lines   []DiffLine
	// NewFile marks a write call that created a file that did not exist
	// before (internal/tools/write.go's writeDetails.NewFile) — the kiln
	// diff block's "new file" tag (docs/kiln-design-handoff/README.md
	// block table, "diff" row).
	NewFile bool
}

// ToolCallView is the data RenderToolCall needs.
type ToolCallView struct {
	Name string
	// PrimaryArg is the one identifying argument, already stringified:
	// the relative path in collapsed mode, absolute in verbose (the
	// caller resolves which).
	PrimaryArg string
	Status     CallStatus
	// ResultLines are summary line(s), already truncated by the caller to
	// the tier's budget. Ignored when Diff is set.
	ResultLines []string
	// Diff, when set, renders as "Added N lines, removed M lines" plus
	// the numbered diff rows instead of ResultLines.
	Diff *ToolDiff
	// TotalLines is the total lines available, when more exist than are
	// shown. Zero means "not set".
	TotalLines int
	// HasTotalLines distinguishes "0 total lines" (never happens in
	// practice) from "not tracked".
	HasTotalLines bool
	// Meta is the label-rule's right-aligned status/timing text, e.g.
	// "approved · 4.1s" (kiln block anatomy). Optional; empty renders no
	// meta. New field — existing callers that build a ToolCallView by name
	// (app.go, bridge.go, replay.go) are unaffected.
	Meta string
}

// MapToolName title-cases a tool id for the call header, with the one
// documented exception: edit renders as "Update"
// (docs/claude-code-reference.md §3: "Tool names are title-cased from the
// tool id except edit → Update").
func MapToolName(id string) string {
	if strings.EqualFold(id, "edit") {
		return "Update"
	}
	return titleCase(id)
}

// toolStatusColor picks the kiln status colour for a tool call's label and
// name: running amber, ok green, error red.
func toolStatusColor(status CallStatus) func(string) string {
	switch status {
	case CallError:
		return KilnRed
	case CallRunning:
		return KilnAmber
	default:
		return KilnGreen
	}
}

// RenderDiffLines renders a diff's numbered rows in the kiln "edit" block
// anatomy (docs/kiln-design-handoff/README.md block table, "diff" row):
// line number (4 columns, right-aligned, Faint), sign (2 columns; "+"
// green, "−" red, context two spaces), then the code (Ink; context lines
// Muted). Added rows get the OnDiffAdd background, removed rows OnDiffDel,
// padded to ruleWidth() so the tint spans a full row (RenderDiffLines has
// no width parameter of its own — see fallbackRuleWidth's doc comment). A
// line too long for the row is truncated with FitStatus, never wrapped —
// the design is explicit that diff lines truncate, unlike every other
// block, because a wrapped diff line loses its line-number alignment.
func RenderDiffLines(lines []DiffLine) []string {
	width := ruleWidth()
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		numStr := fmt.Sprintf("%4d", l.Num)
		var sign, row string
		var bg func(string) string
		switch l.Sign {
		case '+':
			sign = KilnGreen("+ ")
			bg = OnDiffAdd
			row = Faint(numStr) + " " + sign + Ink(l.Text)
		case '-':
			sign = KilnRed("− ")
			bg = OnDiffDel
			row = Faint(numStr) + " " + sign + Ink(l.Text)
		default:
			bg = func(s string) string { return s }
			row = Faint(numStr) + "   " + Muted(l.Text)
		}
		out = append(out, bg(padToWidth(FitStatus(row, width), width)))
	}
	return out
}

// diffHeaderRow renders the diff block's header row on the panel
// background (docs/kiln-design-handoff/README.md block table, "diff" row):
// path, a "new file" tag when set, a flexible spacer, then "+N" green and
// "−N" red, padded to width so the panel tint spans the full row.
func diffHeaderRow(path string, d *ToolDiff, width int) string {
	tag := ""
	if d.NewFile {
		tag = Muted("new file")
	}
	counts := diffCountsRow(d)
	left := " " + Ink(path)
	if tag != "" {
		left += "  " + tag
	}
	pad := width - VisibleWidth(left) - VisibleWidth(counts) - 1
	if pad < 1 {
		pad = 1
	}
	row := left + strings.Repeat(" ", pad) + counts + " "
	return OnPanel(padToWidth(row, width))
}

// diffCountsRow renders "+N  −N", omitting a zero side, for the diff
// header row.
func diffCountsRow(d *ToolDiff) string {
	switch {
	case d.Added > 0 && d.Removed > 0:
		return fmt.Sprintf("%s  %s", KilnGreen("+"+strconv.Itoa(d.Added)), KilnRed("−"+strconv.Itoa(d.Removed)))
	case d.Added > 0:
		return KilnGreen("+" + strconv.Itoa(d.Added))
	case d.Removed > 0:
		return KilnRed("−" + strconv.Itoa(d.Removed))
	default:
		return Muted("no changes")
	}
}

// RenderToolCall renders a tool call and its result in the kiln "tool"/
// "edit" block anatomy (docs/kiln-design-handoff/README.md "Block anatomy"
// and its table):
//
//	edit ─────────────────────────────────────────────────  math.js
//	 math.js  new file                              +2  −0
//	   1 + function add(a,b){ return a + b }
//	   2 + module.exports = { add }
//
//	bash ──────────────────────────────────  approved · 4.1s
//	bash npm test -- upload
//	→ 12 passed
//
// A call with a Diff renders the "edit" label rule (blue, filename meta)
// and the panel header row instead of the "Name arg" line and result rows.
// Otherwise the label rule is the tool name lowercased in the status
// colour (running amber, ok green, err red) with Meta (e.g.
// "approved · 4.1s") as the rule's own meta, and the body is "Name arg"
// followed by the result.
func RenderToolCall(view ToolCallView) []string {
	gl := G()
	statusColor := toolStatusColor(view.Status)
	width := ruleWidth()
	var lines []string
	if view.Diff != nil {
		lines = append(lines, labelRule("edit", KilnBlue, path.Base(view.PrimaryArg), width))
	} else {
		lines = append(lines, labelRule(strings.ToLower(view.Name), statusColor, view.Meta, width))
		lines = append(lines, fmt.Sprintf("%s %s", statusColor(view.Name), Muted(view.PrimaryArg)))
	}

	if view.Diff != nil {
		lines = append(lines, diffHeaderRow(view.PrimaryArg, view.Diff, width))
		lines = append(lines, RenderDiffLines(view.Diff.Lines)...)
		return lines
	}

	body := view.ResultLines
	if len(body) == 0 {
		return lines
	}

	outputColor := Muted
	// Errors: only the lines after the first render red (kiln block
	// anatomy for "tool").
	continuationColor := Muted
	if view.Status == CallError {
		continuationColor = KilnRed
	}

	// Arrow on the first result line only; later lines indent two columns
	// under it.
	lines = append(lines, fmt.Sprintf("%s %s", Muted(gl.Action), outputColor(body[0])))
	for _, extra := range body[1:] {
		lines = append(lines, continuationIndent+continuationColor(extra))
	}

	if view.HasTotalLines && view.TotalLines > len(body) {
		lines = append(lines, continuationIndent+Muted(fmt.Sprintf("… +%d lines (ctrl+o to expand)", view.TotalLines-len(body))))
	}
	return lines
}

// GroupKind is which read-only grouping a collapsed row summarizes.
type GroupKind string

const (
	GroupRead GroupKind = "read" // Read, Glob, Grep, web fetch, Bash without shown output
	GroupBash GroupKind = "bash" // Bash whose output IS shown while running, before it collapses
)

func groupNoun(kind GroupKind, n int) string {
	if kind == GroupBash {
		if n == 1 {
			return "shell command"
		}
		return "shell commands"
	}
	if n == 1 {
		return "file"
	}
	return "files"
}

// RenderToolGroupRunning renders the collapsed row while a read-only group
// is still in flight: a plain "  Reading N file(s)…" row for Read/Glob/
// Grep/web-fetch groups (no marker glyph — Claude Code draws one in
// intermediate frames but the settled row carries none, confirmed against
// spinner.txt), and "⏺ Running N shell command(s)…" with the marker for a
// Bash group (confirmed against spinner-2.txt).
// dimWithBoldCount renders "<prefix><n> <suffix>" muted, with the count
// bold, kiln's restyling of the grouped-read/bash row.
func dimWithBoldCount(prefix string, n int, suffix string) string {
	return Muted(prefix) + Bold(Muted(strconv.Itoa(n))) + Muted(suffix)
}

func RenderToolGroupRunning(kind GroupKind, n int) string {
	switch kind {
	case GroupBash:
		return fmt.Sprintf("%s %s", Muted(G().Call), dimWithBoldCount("Running ", n, " "+groupNoun(kind, n)+"…"))
	default:
		return resultIndent + dimWithBoldCount("Reading ", n, " "+groupNoun(kind, n)+"…")
	}
}

// RenderToolGroupDone renders the collapsed row once a read-only group has
// finished: "  Read N file(s)" / "  Ran N shell command(s)", muted, no
// marker.
func RenderToolGroupDone(kind GroupKind, n int) string {
	verb := "Read"
	if kind == GroupBash {
		verb = "Ran"
	}
	return resultIndent + dimWithBoldCount(verb+" ", n, " "+groupNoun(kind, n))
}

// TodoStatus is a todo item's completion state.
type TodoStatus string

const (
	TodoPendingStatus    TodoStatus = "pending"
	TodoInProgressStatus TodoStatus = "in_progress"
	TodoCompletedStatus  TodoStatus = "completed"
)

// TodoView is one todo list entry. RenderTodos moved to plan.go as
// RenderPlan — kept here only as the shared data shape bridge.go and
// plan.go both use.
type TodoView struct {
	Content string
	Status  TodoStatus
}

var hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// ParseUnifiedDiff turns a unified patch into a ToolDiff: line numbers come
// from the new file for additions and the old file for deletions, so a
// reader can navigate to what they are looking at, and Added/Removed count
// the +/- rows for the summary line.
//
// A real unified patch (internal/tools/editdiff.go's generateUnifiedPatch,
// via go-udiff) starts with "--- path"/"+++ path" file headers before the
// first "@@" hunk; those are skipped rather than misread as -/+ content
// lines, by only looking at +/- rows once a hunk header has been seen.
func ParseUnifiedDiff(patch string, startLine int) *ToolDiff {
	if startLine == 0 {
		startLine = 1
	}
	d := &ToolDiff{}
	oldNo := startLine
	newNo := startLine
	inHunk := false

	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "@@") {
			inHunk = true
			if m := hunkHeaderRe.FindStringSubmatch(line); m != nil {
				oldNo, _ = strconv.Atoi(m[1])
				newNo, _ = strconv.Atoi(m[2])
			}
			continue
		}
		if !inHunk {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"):
			d.Lines = append(d.Lines, DiffLine{Num: newNo, Sign: '+', Text: line[1:]})
			d.Added++
			newNo++
		case strings.HasPrefix(line, "-"):
			d.Lines = append(d.Lines, DiffLine{Num: oldNo, Sign: '-', Text: line[1:]})
			d.Removed++
			oldNo++
		case strings.HasPrefix(line, " "):
			// Context line: kept (dim) so a reader has surrounding lines to
			// orient against (docs/kiln-design-handoff/README.md's diff row:
			// "context lines dim"), numbered against the new file like an
			// addition since it exists at that line in both versions.
			d.Lines = append(d.Lines, DiffLine{Num: newNo, Sign: ' ', Text: line[1:]})
			oldNo++
			newNo++
		default:
			// A blank hunk line (bare "" with no leading space, which
			// go-udiff can emit for a genuinely empty context line) still
			// advances both counters without adding a text line to avoid
			// misnumbering what follows, but is otherwise skipped rather
			// than rendered as a phantom context row.
			oldNo++
			newNo++
		}
	}
	return d
}

// RenderDiff is kept for existing callers that want plain rendered lines
// from a unified patch without the "Added/removed" summary row (e.g. an
// inline permission-prompt preview): it parses the patch and renders only
// the numbered rows.
func RenderDiff(patch string, startLine int) []string {
	return RenderDiffLines(ParseUnifiedDiff(patch, startLine).Lines)
}

// SpinnerArgs is the data RenderSpinner needs.
type SpinnerArgs struct {
	Frame          int
	Label          string
	ElapsedSeconds int
	// Thinking selects the "thinking with <effort> effort" suffix over
	// the plain "(Ns)" one, before token streaming starts.
	Thinking bool
	Effort   string
	// Tokens is nil when there is no live token count yet; once set it
	// takes priority over Thinking, matching a turn that has moved past
	// reasoning into streaming its answer.
	Tokens *int
	// QueueLen is the number of follow-ups queued via Lane.Steer while this
	// turn runs; 0 shows no suffix (docs/kiln-design-handoff/README.md's
	// "Queued follow-up": the busy line's status suffix " · 1 queued").
	QueueLen int
}

// RenderSpinner renders the busy line (docs/kiln-design-handoff/README.md
// "Interactions"):
//
//	◐ Whirring…  4s · 1.2k tokens                              esc to stop
//	◐ Computing…  1s · thinking with medium effort              esc to stop
//	◐ Waiting for approval…                                     esc to stop
//
// Left side (spinner+label KilnAmber, the elapsed/tokens/thinking segment
// Muted) is built by RenderSpinnerLeft; "esc to stop" is right-aligned at
// the caller's width by SpinnerState.Render (app.go/spinner.go own the
// width, RenderSpinner itself is width-agnostic on the right side callers
// that only need the left segment (e.g. a golden of the busy text alone)
// can call RenderSpinnerLeft directly).
func RenderSpinner(args SpinnerArgs) string {
	return RenderSpinnerLeft(args)
}

// RenderSpinnerLeft renders the busy line's left segment only: spinner,
// label, and the elapsed/tokens/thinking suffix. No parentheses, no
// leading "↓" on the token count — the design drops both.
func RenderSpinnerLeft(args SpinnerArgs) string {
	gl := G()
	spin := gl.Spinner[args.Frame%len(gl.Spinner)]
	label := KilnAmber(args.Label + "…")

	var suffix string
	switch {
	case args.Tokens != nil:
		suffix = fmt.Sprintf("%ds · %s tokens", args.ElapsedSeconds, FormatTokens(*args.Tokens))
	case args.Thinking:
		effort := args.Effort
		if effort == "" {
			effort = "medium"
		}
		suffix = fmt.Sprintf("%ds · thinking with %s effort", args.ElapsedSeconds, effort)
	case args.ElapsedSeconds > 0:
		suffix = fmt.Sprintf("%ds", args.ElapsedSeconds)
	}

	if args.QueueLen > 0 {
		queued := fmt.Sprintf("%d queued", args.QueueLen)
		if suffix == "" {
			suffix = queued
		} else {
			suffix += " · " + queued
		}
	}

	if suffix == "" {
		return fmt.Sprintf("%s %s", KilnAmber(spin), label)
	}
	return fmt.Sprintf("%s %s  %s", KilnAmber(spin), label, Muted(suffix))
}

// FormatTokens renders a token count compactly: 450, 3.4k, 1.0m.
func FormatTokens(n int) string {
	if n < 1_000 {
		return strconv.Itoa(n)
	}
	// Million-token windows are ordinary now, and "1000.0k" reads as a
	// mistake.
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fm", float64(n)/1_000_000)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1_000)
}

// spinnerVerb pairs a spinner gerund with its past-tense form for the turn
// summary (docs/claude-code-reference.md §3: "Turn summary matching the
// spinner label family. Observed verbs: Brewed, Crunched, Cooked... Full
// set [chk]"; §6 of the contract names the confirmed family: Brewed,
// Cooked, Crunched, Baked, Churned, Whirred, Simmered, Worked).
//
// Deliberately restricted to exactly these eight: the contract's spinner
// deliverable (§3/§5) lists a longer flavor-word pool (Computing,
// Smooshing, Lollygagging, Thinking, Pondering, Percolating, Noodling,
// Mulling, Ruminating, Cogitating...) but only gives past tenses for
// eight. Rather than invent past tenses for the rest (guessing "Computed"
// or "Smooshed" were never captured), the spinner picks only from this
// eight-word family, so every turn summary is provably correct rather
// than plausible.
type spinnerVerb struct {
	gerund string
	past   string
}

var spinnerVerbs = []spinnerVerb{
	{"Brewing", "Brewed"},
	{"Cooking", "Cooked"},
	{"Crunching", "Crunched"},
	{"Baking", "Baked"},
	{"Churning", "Churned"},
	{"Whirring", "Whirred"},
	{"Simmering", "Simmered"},
	{"Working", "Worked"},
}

// Labels is the spinner's gerund pool, exposed for callers that want the
// list directly (tests, PickLabel's docstring).
var Labels = func() []string {
	out := make([]string, len(spinnerVerbs))
	for i, v := range spinnerVerbs {
		out[i] = v.gerund
	}
	return out
}()

// PickLabel picks a gerund deterministically from a seed, so a turn's
// spinner label and its eventual turn-summary verb are the same pick.
func PickLabel(seed int) string {
	n := seed % len(spinnerVerbs)
	if n < 0 {
		n += len(spinnerVerbs)
	}
	return spinnerVerbs[n].gerund
}

// RenderError renders an error message: red, visually distinct from
// ordinary tool output.
func RenderError(message string) []string {
	lines := strings.Split(strings.TrimRight(message, "\n"), "\n")
	out := make([]string, 0, len(lines)+2)
	// The design's "error" block: a red label rule, then the message in
	// red, one blank row above like every other block.
	out = append(out, "", labelRule("error", KilnRed, "", ruleWidth()))
	for _, line := range lines {
		out = append(out, KilnRed(line))
	}
	return out
}

// ThinkingView is the data RenderThinking needs.
type ThinkingView struct {
	Text string
	// Active means still streaming: the label reads as present tense
	// until it finishes.
	Active   bool
	Expanded bool
}

// RenderThinking renders a reasoning block. Collapsed (default, non-verbose
// mode) shows nothing — the spinner's own suffix carries "thinking with
// <effort> effort" while it streams, and nothing at all once done, per
// docs/claude-code-reference.md §7 ("collapsed mode shows nothing but the
// spinner suffix"). Verbose mode shows a "∴ Thinking" row with the text
// dim underneath.
func RenderThinking(view ThinkingView) []string {
	if !view.Expanded {
		return []string{}
	}
	body := strings.TrimSpace(view.Text)
	if body == "" {
		return []string{}
	}
	gl := G()
	label := "Thinking"
	if view.Active {
		label = "Thinking…"
	}
	out := []string{Muted(fmt.Sprintf("%s %s", gl.Thinking, label))}
	for _, line := range strings.Split(body, "\n") {
		out = append(out, resultIndent+Muted(Italic(line)))
	}
	return out
}

// RenderUserMessage renders the kiln "you" block: a "you" label rule
// (amber, no meta), then the message on the raised surface (`OnRaise`),
// ink text, one blank row above (docs/claude-code-reference.md §3; every
// transcript block starts with a blank row, so the row below the echo
// comes from the next block). The old "❯" prompt glyph is dropped — the
// "you" label identifies the block instead. Used for both a typed prompt
// and a slash-command echo ("you" / "/model") — CommitCommandResult
// (bridge.go) renders the result row that follows a command's echo.
func RenderUserMessage(text string, width int) []string {
	return RenderUserMessageMeta(text, "", width)
}

// RenderUserMessageMeta is RenderUserMessage with an optional label-rule
// meta, e.g. "queued" for a follow-up submitted while a turn is still
// running (docs/kiln-design-handoff/README.md's "Queued follow-up" —
// app.go's handleSubmit commits it this way instead of the plain form).
func RenderUserMessageMeta(text, meta string, width int) []string {
	inner := width - 2
	if inner < 1 {
		inner = 1
	}
	var body []string
	for _, line := range strings.Split(text, "\n") {
		body = append(body, splitLines(ansiWrap(line, inner))...)
	}

	out := make([]string, 0, len(body)+2)
	out = append(out, "")
	out = append(out, labelRule("you", KilnAmber, meta, width))
	for _, wl := range body {
		out = append(out, OnRaise(padToWidth(Ink(wl), width)))
	}
	return out
}

// PrimaryArg is the one identifying argument for a call line. Claude Code
// shows Read(src/foo.ts), not a serialized argument object. These keys are
// tried in order of how well they identify the call.
func PrimaryArg(args any) string {
	if s, ok := args.(string); ok {
		return s
	}
	obj, ok := args.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"path", "file_path", "filePath", "command", "pattern", "query", "url"} {
		if v, ok := obj[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

// Summarize pulls the displayable text out of a tool result.
//
// The important case is `content`, which is an ARRAY of blocks
// ([{type:"text", text:"..."}]), not a string — that is the shape every
// tool in this harness returns. Treating it as a string falls through to
// JSON, which renders the whole result as one escaped line. Blocks that
// are not text (images, structured details) are skipped rather than
// stringified, since the transcript has no way to show them inline anyway.
func Summarize(result any) []string {
	if result == nil {
		return []string{}
	}
	switch v := result.(type) {
	case string:
		return summarizeLines(v)
	case []any:
		return blockText(v)
	case map[string]any:
		// `output` and `text` are plain strings when present; `content` is
		// the block array. Checked in that order because a tool that sets
		// both means the string as the human-facing form.
		if s, ok := v["output"].(string); ok {
			return summarizeLines(s)
		}
		if s, ok := v["text"].(string); ok {
			return summarizeLines(s)
		}
		if s, ok := v["content"].(string); ok {
			return summarizeLines(s)
		}
		if arr, ok := v["content"].([]any); ok {
			return blockText(arr)
		}
		// Last resort, and now genuinely a last resort rather than the
		// common path.
		b, err := json.Marshal(v)
		if err != nil {
			return []string{}
		}
		return strings.Split(string(b), "\n")
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return []string{}
		}
		return strings.Split(string(b), "\n")
	}
}

// summarizeLines splits, dropping the empty final element a trailing
// newline produces. Command output almost always ends in a newline;
// kept, it renders as a blank row under every tool call.
func summarizeLines(text string) []string {
	out := strings.Split(text, "\n")
	if len(out) > 1 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

func blockText(blocks []any) []string {
	var parts []string
	for _, b := range blocks {
		switch bl := b.(type) {
		case string:
			if bl != "" {
				parts = append(parts, bl)
			}
		case map[string]any:
			if bl["type"] == "text" {
				if s, ok := bl["text"].(string); ok && s != "" {
					parts = append(parts, s)
				}
			}
		}
	}
	if len(parts) == 0 {
		return []string{}
	}
	return summarizeLines(strings.Join(parts, "\n"))
}

// RenderAssistantText renders the kiln "text" block: a "kiln" label rule
// (muted, no meta) above the already-rendered markdown lines (e.g. from
// MarkdownRenderer.Render, which carries its own ink/dim/amber/green/blue
// colouring). The old "⏺ " marker is dropped — the "kiln" label identifies
// the block instead, same as "you" replaced "❯". There is no separate
// streaming-caret variant of this function today (no call site passes a
// streaming flag — the live region during streaming is owned by
// bridge.go/app.go, outside this file's scope), so the trailing "▍" caret
// the design calls for while streaming is not added here.
func RenderAssistantText(lines []string) []string {
	if len(lines) == 0 {
		return lines
	}
	width := ruleWidthFor(lines)
	out := make([]string, 0, len(lines)+1)
	out = append(out, labelRule("kiln", Muted, "", width))
	out = append(out, lines...)
	return out
}

// RenderVerboseModelRow renders the right-aligned dim row Claude Code
// shows after a turn's last tool call in verbose mode:
//
//	10:03 AM claude-opus-5
//
// (docs/claude-code-reference.md §3/§7). Right-aligned to width.
func RenderVerboseModelRow(done time.Time, modelID string, width int) string {
	if done.IsZero() {
		done = time.Now()
	}
	text := fmt.Sprintf("%s %s", done.Format("3:04 PM"), modelID)
	// The row sits eight columns short of the right edge, not flush against
	// it (verbose-ctrl-o.txt row 21 ends at column 92 of a 100-wide
	// terminal — a right margin Claude Code leaves on this row alone).
	const rightMargin = 8
	pad := width - rightMargin - VisibleWidth(text)
	if pad < 0 {
		pad = 0
	}
	return strings.Repeat(" ", pad) + Muted(text)
}
