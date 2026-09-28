package tui

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/andrepato/harness/internal/msg"
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
// is approximate for these blocks. RenderUserMessage, which already takes
// width, uses it for real.
const fallbackRuleWidth = 56

// renderWidth is the live terminal content width, set by the app on every
// WindowSizeMsg (SetRenderWidth). The width-less Render* functions size
// their label rules and row-background tints to it so kiln's hairlines
// fill the full width. It falls back to fallbackRuleWidth before the first
// size message (e.g. in unit tests that call a renderer directly).
var renderWidth int

// renderMargin is the live left margin (layout_margin.go), set alongside
// renderWidth by the app on every WindowSizeMsg (SetRenderMargin). Read by
// CommitNote (note.go), the one width-less block that renders and commits
// in the same call, so no app.go caller ever gets its lines back to
// margin-pad itself the way m.commit/m.commitSynthetic do.
var renderMargin int

// SetRenderWidth records the current terminal content width for the
// width-less Render* helpers. A non-positive value is ignored.
func SetRenderWidth(w int) {
	if w > 0 {
		renderWidth = w
	}
}

// SetRenderMargin records the current left margin for CommitNote. Negative
// values are ignored; 0 (the narrow-terminal case) is valid.
func SetRenderMargin(cols int) {
	if cols >= 0 {
		renderMargin = cols
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

// ruleMargin is CommitNote's counterpart to ruleWidth: the left margin to
// pad its rows by.
func ruleMargin() int {
	return renderMargin
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
	Sign byte // '-', '+', ' ' for an unchanged context line, or '~' for a gap between hunks
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
		case '~':
			out = append(out, padToWidth(Faint("   …"), width))
			continue
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

// diffCountsRow renders "+N  −N" for the diff header row. Both sides
// always show, even when one is zero (a new file is all additions, so its
// removed count is 0, not absent) — Terminal.dc.html's stat builder sets
// both d.addS and d.delS unconditionally (line ~364: d.addS='+'+a;
// d.delS='−'+r), and qa/reference/diff.png shows both signs together.
// Defect: this used to omit whichever side was 0, which for a new-file
// diff meant no "−0" ever appeared at all (finding
// diff-header-omits-zero-side).
func diffCountsRow(d *ToolDiff) string {
	if d.Added == 0 && d.Removed == 0 {
		return Muted("no changes")
	}
	return fmt.Sprintf("%s  %s", KilnGreen("+"+strconv.Itoa(d.Added)), KilnRed("−"+strconv.Itoa(d.Removed)))
}

// collapsedSummary is the result summary a committed tool block shows
// outside verbose mode. A loaded skill's text is instructions for the
// model, not output for the reader: it reads "loaded · N lines" (ctrl+o
// shows it all). Everything else is unchanged. The live and replay block
// builders both call this, so a toggle renders the same block.
func collapsedSummary(toolName string, summary []string, failed, verbose bool) []string {
	if !verbose && !failed && strings.EqualFold(toolName, "skill") && len(summary) > 0 {
		return []string{fmt.Sprintf("loaded · %d lines", len(summary))}
	}
	return summary
}

// clipResultLines clips a tool result's summary lines to max the same way
// every committed tool block does (kiln's "tool" row: first N lines, then
// "… +N lines (ctrl+o to expand)") — except for a failed call, where the
// single most important line is the LAST one, not the first: bash.go
// appends the exit-code/timeout/abort status after the command's own
// output, so keeping only the first max lines silently buries that status
// behind the collapse cutoff for any failure with more than max-1 lines of
// output (defect 20260926T235921Z-bash-exit-code-message-hidden-by-
// collapse). Both committed-tool-block builders (bridge.go's EventToolEnd
// handler, replay.go's toolCallViewFor) call this instead of the bare
// clipTo, so a failed call's collapsed view always ends with its own exit
// status, live or replayed alike.
func clipResultLines(lines []string, max int, status CallStatus) []string {
	if len(lines) <= max || max <= 0 {
		return clipTo(lines, max)
	}
	if status != CallError {
		return lines[:max]
	}
	if max == 1 {
		return lines[len(lines)-1:]
	}
	out := make([]string, 0, max)
	out = append(out, lines[:max-1]...)
	out = append(out, lines[len(lines)-1])
	return out
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
//	npm test -- upload
//	→ 12 passed
//
// A call with a Diff renders the "edit" label rule (blue, filename meta)
// and the panel header row instead of the argument line and result rows.
// Otherwise the label rule is the tool name lowercased in the status
// colour (running amber, ok green, err red) with Meta (e.g.
// "approved · 4.1s") as the rule's own meta, and the body is the argument
// followed by the result. The design repeats the tool name before the
// argument; Andre dropped it as printing the name twice.
func RenderToolCall(view ToolCallView) []string {
	gl := G()
	statusColor := toolStatusColor(view.Status)
	width := ruleWidth()
	var lines []string
	if view.Diff != nil {
		lines = append(lines, labelRule("edit", KilnBlue, path.Base(view.PrimaryArg), width))
	} else {
		lines = append(lines, labelRule(strings.ToLower(view.Name), statusColor, view.Meta, width))
		// The label rule already names the tool; repeating it on the row
		// below ("read ───" then "Read FEATURE.md") printed it twice.
		if view.PrimaryArg != "" {
			lines = append(lines, Ink(view.PrimaryArg))
		}
	}

	if view.Diff != nil {
		lines = append(lines, diffHeaderRow(view.PrimaryArg, view.Diff, width))
		lines = append(lines, RenderDiffLines(view.Diff.Lines)...)
		return lines
	}

	body := view.ResultLines
	// Whitespace-only output (a command that printed a bare newline) drew
	// a "→" with nothing after it.
	if strings.TrimSpace(strings.Join(body, "")) == "" && (!view.HasTotalLines || view.TotalLines <= len(body)) {
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
	hidden := 0
	if view.HasTotalLines && view.TotalLines > len(body) {
		hidden = view.TotalLines - len(body)
	}
	hint := continuationIndent + Muted(fmt.Sprintf("… +%d lines (ctrl+o to expand)", hidden))
	// A clipped failed result keeps its last line (clipResultLines), and
	// the hidden lines sit before it, so the hint goes there too.
	tailKept := hidden > 0 && view.Status == CallError && len(body) > 1
	lines = append(lines, fmt.Sprintf("%s %s", Muted(gl.Action), outputColor(body[0])))
	for i, extra := range body[1:] {
		if tailKept && i == len(body)-2 {
			lines = append(lines, hint)
		}
		lines = append(lines, continuationIndent+continuationColor(extra))
	}
	if hidden > 0 && !tailKept {
		lines = append(lines, hint)
	}
	return lines
}

// CompactReadGroup reports whether a run of consecutive read-only calls
// commits as one block (RenderReadGroup) rather than one block each: two
// or more calls, every one a read-kind tool that succeeded. A failed call
// keeps its full block so its error stays visible; verbose mode (ctrl+o)
// never compacts, so expanding shows every call's own output.
func CompactReadGroup(views []ToolCallView, verbose bool) bool {
	if verbose || len(views) < 2 {
		return false
	}
	for _, v := range views {
		kind, ok := groupKindFor(v.Name)
		if !ok || kind != GroupRead || v.Status != CallOK || v.Diff != nil {
			return false
		}
	}
	return true
}

// RenderReadGroup renders consecutive read-only calls as one "read" block:
// the label rule with the call count as meta, then one "arg · size"
// row per call. Four file reads were four blocks of five rows each, each
// previewing the first lines of a file nobody asked to see.
//
//	read ─────────────────────────────────────────────  4 files
//	web/src/api.ts · 56 lines
//	web/src/App.tsx · 293 lines
func RenderReadGroup(views []ToolCallView) []string {
	allRead := true
	for _, v := range views {
		if !strings.EqualFold(v.Name, "read") {
			allRead = false
		}
	}
	meta := fmt.Sprintf("%d calls", len(views))
	if allRead {
		meta = fmt.Sprintf("%d files", len(views))
	}
	lines := []string{labelRule("read", toolStatusColor(CallOK), meta, ruleWidth())}
	for _, v := range views {
		// A Read row is just its path under the "read" label; a Grep or
		// Glob row keeps its name, which the label does not say.
		row := Ink(v.PrimaryArg)
		if !strings.EqualFold(v.Name, "read") {
			row = fmt.Sprintf("%s %s", toolStatusColor(CallOK)(v.Name), Ink(v.PrimaryArg))
		}
		if size := readGroupSize(v); size != "" {
			row += Faint(" · " + size)
		}
		lines = append(lines, row)
	}
	return lines
}

// readGroupSize is a grouped call's one-word result: the file's line count
// for a read, the number of result lines for a search or listing.
func readGroupSize(v ToolCallView) string {
	n := len(v.ResultLines)
	if v.HasTotalLines {
		n = v.TotalLines
	}
	plural := func(n int, one, many string) string {
		if n == 1 {
			return "1 " + one
		}
		return fmt.Sprintf("%d %s", n, many)
	}
	switch strings.ToLower(v.Name) {
	case "read":
		return plural(n, "line", "lines")
	case "grep", "glob", "ls":
		if n == 0 {
			return "no results"
		}
		return plural(n, "result", "results")
	}
	return ""
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
			// A second hunk is elsewhere in the file: mark the gap, or the
			// two hunks read as one change with jumping line numbers.
			if len(d.Lines) > 0 {
				d.Lines = append(d.Lines, DiffLine{Sign: '~'})
			}
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
	// Wrapped, never clipped: provider errors carry a raw response body
	// whose tail (the error type) is the part worth reading.
	lines := wrapMultiline(strings.TrimRight(message, "\n"), ruleWidth())
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

// RenderThinking renders a reasoning block in the design's label-rule
// language (docs/kiln-design-handoff/README.md "Block anatomy": a dim
// label on a hairline rule, like the "you"/"kiln"/tool blocks use) instead
// of the old ad hoc "∴ Thinking" line — ∴ is dropped outright, since it is
// not in the design's glyph set. Collapsed (default) shows the label rule
// and a one-line summary — the reasoning's first line, width-truncated
// when even that one line overflows the row — plus a "ctrl+o to expand"
// hint whenever anything is actually hidden (more lines, or the first one
// truncated); a short, single-line reasoning that already fits gets no
// hint, since there is nothing ctrl+o would reveal. Expanded shows the
// label rule and the full reasoning text, dim and italic. The previous
// version returned nothing at all unless Expanded was already true, and
// nothing ever set Expanded true on a committed block (app.go's
// handleThinking, ended case) — so a thinking block was invisible in
// every state, never reachable by ctrl+o (defect
// 20260926T232657Z-thinking-invisible).
func RenderThinking(view ThinkingView) []string {
	body := strings.TrimSpace(view.Text)
	if body == "" {
		return []string{}
	}
	label := "thinking"
	if view.Active {
		label = "thinking…"
	}
	width := ruleWidth()
	lines := strings.Split(body, "\n")
	out := []string{labelRule(label, Muted, "", width)}
	if !view.Expanded {
		// A fixed preview budget, not "whatever fits in this terminal": a
		// wide terminal must still collapse a longish reasoning line to a
		// genuine summary rather than rendering it in full just because it
		// happens to fit the row, or "collapsed" and "expanded" would be
		// identical on anything but a narrow screen.
		const previewChars = 80
		budget := previewChars
		if w := width - VisibleWidth(resultIndent); w < budget {
			budget = w
		}
		if budget < 10 {
			budget = 10
		}
		first := lines[0]
		summary := FitStatus(first, budget)
		truncatedFirstLine := VisibleWidth(summary) < VisibleWidth(first)
		hiddenLines := len(lines) - 1
		out = append(out, resultIndent+Muted(Italic(summary)))
		switch {
		case hiddenLines > 0:
			out = append(out, resultIndent+Muted(fmt.Sprintf("… +%d lines (ctrl+o to expand)", hiddenLines)))
		case truncatedFirstLine:
			out = append(out, resultIndent+Muted("(ctrl+o to expand)"))
		}
		return out
	}
	for _, line := range lines {
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

// userBlockPad is the you-block's inner horizontal padding, each side
// (Terminal.dc.html:52, "padding:3px 10px" — 10px at 14px Fira Code
// (~8.4px/col) is ~1 column), applied on top of whatever outer margin the
// caller already indented the block by.
const userBlockPad = 1

// RenderUserMessageMeta is RenderUserMessage with an optional label-rule
// meta, e.g. "queued" for a follow-up submitted while a turn is still
// running (docs/kiln-design-handoff/README.md's "Queued follow-up" —
// app.go's handleSubmit commits it this way instead of the plain form).
//
// The raised surface spans the full block width (not just the text cells,
// finding you-block-surface-width) and insets the text by userBlockPad on
// both sides (finding you-block-no-inner-padding): pad the plain text
// first, colour it, then raise the whole already-single-span result last —
// wrapping OnRaise around an independently pre-rendered (and so already
// reset-terminated) span is what let the background die at the text's own
// end instead of reaching the padding; see onRaiseSpan's doc comment for
// the general form this needs when more than one foreground colour is
// involved.
func RenderUserMessageMeta(text, meta string, width int) []string {
	inner := width - 2*userBlockPad
	if inner < 1 {
		inner = 1
	}
	var body []string
	for _, line := range strings.Split(text, "\n") {
		body = append(body, splitLines(ansiWrap(line, inner))...)
	}

	pad := strings.Repeat(" ", userBlockPad)
	out := make([]string, 0, len(body)+2)
	out = append(out, "")
	out = append(out, labelRule("you", KilnAmber, meta, width))
	for _, wl := range body {
		row := padToWidth(pad+wl, width)
		out = append(out, OnRaise(Ink(row)))
	}
	return out
}

// RenderQueuedFollowUp renders a follow-up submitted via Lane.Steer while a
// turn is busy, for the live region (app.go's liveTail) rather than the
// committed transcript: a dim "you" label rule with a "queued" meta, dim
// body text — matching the design's label-rule language
// (docs/kiln-design-handoff/README.md "Block anatomy": label, rule, meta)
// but muted throughout, since it is not committed yet. It renders in place
// of RenderUserMessageMeta's "queued" form, which used to commit the block
// immediately and read out of order, above the reply to the turn it
// interrupted (defect 20260926T232249Z-queued-block-order) — the lane's own
// drain (turn.go's drainInbox) is what turns this into a real, ordinary
// RenderUserMessage block, at the point it actually lands on the branch.
func RenderQueuedFollowUp(text string, width int) []string {
	inner := width - 2*userBlockPad
	if inner < 1 {
		inner = 1
	}
	var body []string
	for _, line := range strings.Split(text, "\n") {
		body = append(body, splitLines(ansiWrap(line, inner))...)
	}

	pad := strings.Repeat(" ", userBlockPad)
	out := make([]string, 0, len(body)+2)
	out = append(out, "")
	out = append(out, labelRule("you", Muted, "queued", width))
	for _, wl := range body {
		// No raised background here (unlike RenderUserMessageMeta): a
		// queued follow-up previews an item not yet committed to the
		// branch, and stays fully muted rather than reading as a landed
		// "you" block — but it wraps and insets the same way so the two
		// forms read as the same block shape once it does land.
		out = append(out, Muted(pad+wl))
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
	for _, key := range []string{"path", "file_path", "filePath", "command", "pattern", "query", "url", "skill", "id"} {
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

// RenderVerboseModelRow (the right-aligned "10:03 AM claude-opus-5" row
// after a turn's last tool call in verbose mode) is gone: the kiln design's
// "text" block has no meta column (docs/kiln-design-handoff/README.md
// "Block anatomy" table — "kiln (dim) | – |") for it to sit on, and as its
// own row it was defect 20260926T232657Z-verbose-toggle-inconsistent's
// stray "7:00 PM faux/faux-1" line, dropped per that defect's own "or is
// dropped if the design has no place for it". Its two call sites
// (app.go's finishTurn, replay.go's RenderTranscriptEntries) are removed
// along with it.

// --- provider blocks (Anthropic web_search) ------------------------------

// anthropicServerToolUseBlock/anthropicWebSearchResultBlock decode the raw
// JSON a msg.ProviderBlock carries for Provider=="anthropic", mirroring
// the wire shapes internal/provider/api/anthropic_messages.go builds.
type anthropicServerToolUseBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type anthropicWebSearchResultBlock struct {
	Type      string          `json:"type"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

type anthropicSearchResultItem struct {
	Type  string `json:"type"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

type anthropicSearchErrorItem struct {
	Type      string `json:"type"`
	ErrorCode string `json:"error_code"`
}

// searchQueriesByToolUseID scans an assistant message's content for
// server_tool_use blocks and returns their queries keyed by block id, so
// the paired web_search_tool_result block (matched on tool_use_id) can
// show what was searched for.
func searchQueriesByToolUseID(content msg.Blocks) map[string]string {
	out := map[string]string{}
	for _, c := range content {
		pb, ok := c.(msg.ProviderBlock)
		if !ok || pb.Provider != "anthropic" {
			continue
		}
		var b anthropicServerToolUseBlock
		if json.Unmarshal(pb.Raw, &b) != nil || b.Type != "server_tool_use" || b.Name != "web_search" {
			continue
		}
		var input struct {
			Query string `json:"query"`
		}
		if json.Unmarshal(b.Input, &input) == nil {
			out[b.ID] = input.Query
		}
	}
	return out
}

// searchResultView builds the committed block for one Anthropic
// web_search_tool_result ProviderBlock: label "Web Search", the query as
// the primary arg, and one result row — "→ N results · domain, domain, …"
// on success, the error code on failure. ok is false when pb is not a
// web_search_tool_result block at all (a server_tool_use block never gets
// its own row; its query is folded into the paired result's view instead).
func searchResultView(pb msg.ProviderBlock, queries map[string]string) (ToolCallView, bool) {
	if pb.Provider != "anthropic" {
		return ToolCallView{}, false
	}
	var b anthropicWebSearchResultBlock
	if json.Unmarshal(pb.Raw, &b) != nil || b.Type != "web_search_tool_result" {
		return ToolCallView{}, false
	}
	view := ToolCallView{
		Name:       MapToolName("web_search"),
		PrimaryArg: queries[b.ToolUseID],
		Status:     CallOK,
	}
	var errItem anthropicSearchErrorItem
	if json.Unmarshal(b.Content, &errItem) == nil && errItem.Type == "web_search_tool_result_error" {
		view.Status = CallError
		view.ResultLines = []string{"→ " + errItem.ErrorCode}
		return view, true
	}
	var items []anthropicSearchResultItem
	if err := json.Unmarshal(b.Content, &items); err != nil {
		// An unrecognized content shape still commits a block rather than
		// silently dropping the search from the transcript.
		view.ResultLines = []string{"→ 0 results"}
		return view, true
	}
	const maxDomains = 5
	domains := make([]string, 0, maxDomains)
	seen := map[string]bool{}
	for _, it := range items {
		if len(domains) >= maxDomains {
			break
		}
		host := it.URL
		if u, err := url.Parse(it.URL); err == nil && u.Hostname() != "" {
			host = u.Hostname()
		}
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		domains = append(domains, host)
	}
	line := fmt.Sprintf("→ %d result", len(items))
	if len(items) != 1 {
		line += "s"
	}
	if len(domains) > 0 {
		line += " · " + strings.Join(domains, ", ")
	}
	view.ResultLines = []string{line}
	return view, true
}
