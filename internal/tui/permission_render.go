package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Inline permission prompt rendering, ported from the render half of
// permission-prompt.ts:193-266 (the pure lines; PermissionPromptView's key
// handling and promise plumbing belong to the Bubbletea model built in a
// later phase, not to this pure-function package).
//
// Parity spec §8: an inline block in the transcript flow, not a modal. A
// modal hides the conversation you are being asked about, which is
// precisely the context needed to answer.

// PermissionRequest is what a permission prompt is asking about.
type PermissionRequest struct {
	ToolName         string
	PrimaryArg       string
	OutsideWorkspace bool
	Args             map[string]any
}

// declinedNoteText builds the "✕ Declined …" note's text for a denied
// tool-permission request (docs/kiln-design-handoff/README.md "note" row):
// bash reads as just the declined command ("✕ Declined npm test -- upload",
// no "Bash" prefix — the command already reads as an action); every other
// tool keeps its mapped name ahead of the argument ("✕ Declined Update
// src/math.js").
func declinedNoteText(req PermissionRequest) string {
	if strings.EqualFold(req.ToolName, "bash") {
		return "✕ Declined " + req.PrimaryArg
	}
	return "✕ Declined " + MapToolName(req.ToolName) + " " + req.PrimaryArg
}

// SummarizeArg truncates a long argument for display without hiding what
// is being approved.
//
// Absolute paths are shortened relative to cwd: a 70-character temp path
// pushes the part that matters off the line, and the prompt is unreadable
// if the thing being approved does not fit on screen.
func SummarizeArg(req PermissionRequest, cwd string) string {
	arg := req.PrimaryArg
	if strings.HasPrefix(arg, "/") {
		if rel, err := filepath.Rel(cwd, arg); err == nil && rel != "" && !strings.HasPrefix(rel, "..") {
			arg = rel
		}
	}
	if len(arg) <= 200 {
		return arg
	}
	// Keep the head: the dangerous part of a command is almost always at
	// the front, and a tail-truncated command reads as something
	// different.
	return fmt.Sprintf("%s… (%d chars)", arg[:200], len(arg))
}

// RenderPermissionPrompt renders the permission prompt for one request,
// fitted to width.
//
// feedbackMode is true while the user is typing a reason after choosing
// "no"; feedback is the text typed so far. cwd is used to relativize a
// path argument via SummarizeArg. Fitting happens once, here, rather than
// at each line-building site above it: almost everything this prompt
// shows is content from elsewhere — a bash command, a diff hunk, a line
// the user is typing — so any of it can be wider than the terminal.
func RenderPermissionPrompt(req PermissionRequest, cwd string, width int, selected int, feedbackMode bool, feedback string) []string {
	amberRule := KilnAmber(strings.Repeat(RuleFillChar(), maxInt(width, 1)))
	lines := []string{
		"",
		labelRule("approval needed", KilnAmber, "", width),
		amberRule,
		"",
		"  " + KilnAmber(Bold(fmt.Sprintf("Allow kiln to use %s?", req.ToolName))),
	}
	if req.PrimaryArg != "" {
		lines = append(lines, "  "+raisedCommand(SummarizeArg(req, cwd), maxInt(width-4, 1)))
	}

	// The reason for the prompt changes what the answer should be, so say
	// it.
	if req.OutsideWorkspace {
		lines = append(lines, "  "+Muted("outside the workspace"))
	}

	// Show the actual change for edits and writes. A path alone says
	// nothing about whether this is a typo fix or a file being emptied.
	if preview := RenderChangePreview(req.ToolName, req.Args); preview != nil {
		lines = append(lines, "")
		lines = append(lines, preview...)
	}
	lines = append(lines, "")

	if feedbackMode {
		lines = append(lines,
			"  "+Muted("What should be done instead?"),
			fmt.Sprintf("  %s %s%s", KilnAmber(">"), feedback, Faint("▌")),
			"  "+Muted("enter to send · esc to decline without a reason"),
			amberRule,
		)
		return FitLines(lines, width, "    ")
	}

	lines = append(lines,
		"  "+permissionOptionRow("1", "Yes", selected == 0, maxInt(width-2, 1)),
		"  "+permissionOptionRow("2", "Yes, and don't ask again for this", selected == 1, maxInt(width-2, 1)),
		"  "+permissionOptionRow("3", "No, and tell kiln what to do instead", selected == 2, maxInt(width-2, 1)),
		"",
		"  "+Muted("↑↓ select · enter confirm · esc decline"),
		amberRule,
	)
	return FitLines(lines, width, "    ")
}

// permissionOptionRow renders one numbered permission option per kiln's
// selection model: when selected, the *whole* row — not just the key — sits
// on the raised background, amber key next to an ink label (Terminal.dc.
// html:80's `<div>` on `{{o.bg}}`, the whole row, not a per-token tint);
// otherwise a plain row with a faint key and dim text. Neither branch adds
// its own leading indent — every call site already puts a fixed " " or "  "
// ahead of it, matching that block's sibling lines (the question, the
// hint), and width is the row's own available width with that indent
// already subtracted, so the raised span reaches the same right edge every
// other full-width block does without shifting the row's left column.
//
// Built from onRaiseSpan (see its doc comment): the key and the label are
// two different foreground colours over the same background, so wrapping
// OnRaise around content that already went through KilnAmber/Ink's own
// Render() calls would lose the background at each of those inner resets —
// exactly the bug this row had before (raising only the key: the key's own
// OnRaise+colour wrap ended, in ANSI terms, before the label and the rest
// of the row began).
func permissionOptionRow(key, label string, selected bool, width int) string {
	if !selected {
		return Faint(key) + "  " + Muted(label)
	}
	content := key + "  " + label
	if !IsColorEnabled() {
		// No background to justify a right-hand fill when colour is off
		// (screen-reader/plain mode, or a plain-text reference capture) —
		// only the key+label, exactly as before this row raised full
		// width.
		return content
	}
	trailing := width - VisibleWidth(content)
	if trailing < 0 {
		trailing = 0
	}
	return onRaiseSpan(textHex.Amber, key) +
		onRaiseSpan(textHex.Ink, "  "+label) +
		onRaiseSpan("", strings.Repeat(" ", trailing))
}

// maxInt returns the larger of a and b, used to keep rule/pad widths from
// going non-positive on a very narrow terminal.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// BashPermissionRequest describes a bash command awaiting approval.
type BashPermissionRequest struct {
	Command     string
	Description string // omitted row when empty
	Cwd         string
}

// bashDontAskRule is the gate expression option 2 offers: the first two
// words of the command plus " *", matching Bash(<prefix> *) semantics —
// e.g. "openssl rand -hex 4" -> "openssl rand *".
func bashDontAskRule(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return "*"
	}
	if len(fields) == 1 {
		return fields[0] + " *"
	}
	return fields[0] + " " + fields[1] + " *"
}

// RenderBashPermissionPrompt renders the Bash command permission prompt
// exactly as captured in testdata/reference/claude-code/permission-bash.txt
// (rows 12-27, captured at terminal width 100; verified in
// permission_render_test.go by diffing this function's output against
// that file's rows). Rendered inline in place of the input box; the
// transcript rows above it ("⏺ <tool description>" / "  ⎿  $ <command>")
// belong to the transcript renderer, not this function.
//
// Styling (Bold on the selected option, KilnAmber on its "❯") is not
// independently colour-verified for this screen — permission-bash.txt is
// a plain-text capture with no SGR — so it is a reuse of the same kiln
// amber accent theme.go documents for a selected row elsewhere.
// [chk].
func RenderBashPermissionPrompt(req BashPermissionRequest, width, selected int) []string {
	amberRule := KilnAmber(strings.Repeat(RuleFillChar(), maxInt(width, 1)))
	lines := []string{
		labelRule("approval needed", KilnAmber, "", width),
		amberRule,
		" " + KilnAmber(Bold("Allow kiln to run this command?")),
		"",
		" " + raisedCommand(req.Command, maxInt(width-2, 1)),
	}
	if req.Description != "" {
		lines = append(lines, "   "+Muted(req.Description))
	}
	lines = append(lines, "")

	options := []string{
		"Yes",
		"Yes, and don’t ask again for: " + bashDontAskRule(req.Command),
		"Yes, and switch to auto mode · auto mode handles these prompts for you",
		"No",
	}
	for i, opt := range options {
		key := fmt.Sprintf("%d", i+1)
		lines = append(lines, " "+permissionOptionRow(key, opt, i == selected, maxInt(width-1, 1)))
	}

	lines = append(lines, "", " "+Muted("↑↓ select · enter confirm · esc decline · tab to amend"), amberRule)
	// Fit every row to width, same as the generic prompt path
	// (RenderPermissionPrompt above returns FitLines(...) rather than
	// lines directly) — this function used to return its lines raw, so a
	// long option row (e.g. "Yes, and switch to auto mode · ...") ran
	// past the right edge uncut on a narrow terminal instead of wrapping
	// (finding bash-prompt-option-overflow-narrow-terminal). "    " lines
	// up a wrapped continuation under the option label, matching
	// permissionOptionRow's "key + two spaces" prefix.
	return FitLines(lines, width, "    ")
}

// EditKind distinguishes the Edit vs Write permission prompt's header and
// question wording. Deliverable 6 states only that "Write prompt mirrors
// Edit with ' Write file'"; the question-line wording for Write below
// ("Do you want to write to <path>?") is not backed by a reference
// capture of the Write prompt (none exists in testdata/reference) — [chk].
type EditKind int

const (
	EditKindEdit EditKind = iota
	EditKindWrite
)

// DiffHunk is one changed line shown in the edit-permission prompt's diff,
// as " {num} -{old}" / " {num} +{new}" rows.
//
// Old/New == "" means that side is a genuine blank line and still renders
// (as " {num} -" / " {num} +" with no trailing text) — otherwise a
// written file with blank lines renders with its line numbers skipping,
// which disagrees with how the committed diff block renders the same
// file afterward (defect 2). OldAbsent/NewAbsent is the actual "this
// side does not exist at all" signal: true for every hunk the Write
// prompt builds (a pure addition has no old side, ever) and for the
// tail of whichever side is shorter in an Edit's file-based diff. Both
// default to false so a hunk literal that only sets Old/New (the common,
// symmetric case) renders exactly as before.
type DiffHunk struct {
	LineNum   int
	Old       string
	New       string
	OldAbsent bool
	NewAbsent bool
}

// EditPermissionRequest describes an Edit or Write call awaiting approval.
type EditPermissionRequest struct {
	Kind  EditKind
	Path  string // relative path, already summarized by the caller
	Hunks []DiffHunk
}

// RenderEditPermissionPrompt renders the Edit/Write permission prompt
// exactly as testdata/reference/claude-code/permission-edit.txt rows
// 16-29 (captured at terminal width 100; verified in
// permission_render_test.go by diffing this function's output against
// that file's rows for the Edit case). Rendered inline in place of the
// input box; the tool-call header ("⏺ Update(math.js)") stays in the
// transcript, owned elsewhere.
//
// feedbackMode/feedback reuse the same "what should be done instead"
// capture pattern RenderPermissionPrompt already uses elsewhere in this
// file — there is no reference capture of the edit prompt's feedback
// state to diff against, so that specific wording is [chk].
func RenderEditPermissionPrompt(req EditPermissionRequest, width, selected int, feedbackMode bool, feedback string) []string {
	verb := "edit"
	if req.Kind == EditKindWrite {
		verb = "write to"
	}

	amberRule := KilnAmber(strings.Repeat(RuleFillChar(), maxInt(width, 1)))
	dashedRule := Rule(strings.Repeat("╌", maxInt(width, 1)))

	numWidth := 1
	for _, h := range req.Hunks {
		w := len(fmt.Sprintf("%d", h.LineNum))
		if w > numWidth {
			numWidth = w
		}
	}

	lines := []string{
		labelRule("approval needed", KilnAmber, "", width),
		amberRule,
		" " + KilnAmber(Bold(fmt.Sprintf("Allow kiln to %s %s?", verb, req.Path))),
		dashedRule,
	}
	// Sign-to-code spacing matches the committed diff block
	// (transcript.go's RenderDiffLines: KilnGreen("+ ")/KilnRed("− "), sign
	// then a space, not glued to the text) and the design's 2ch sign
	// column (Terminal.dc.html's diff row template). Previously this used
	// a bare ASCII "-"/"+" with no space, so the same diff read
	// differently depending on whether it was pending approval or already
	// committed (finding diff-sign-spacing-prompt-vs-committed).
	var hunkLines []string
	for _, h := range req.Hunks {
		if !h.OldAbsent {
			hunkLines = append(hunkLines, " "+Faint(fmt.Sprintf("%*d", numWidth, h.LineNum))+" "+KilnRed("− "+h.Old))
		}
		if !h.NewAbsent {
			hunkLines = append(hunkLines, " "+Faint(fmt.Sprintf("%*d", numWidth, h.LineNum))+" "+KilnGreen("+ "+h.New))
		}
	}
	// Cap the rows actually shown so the question, the path and the
	// options always stay on one screen (defect 1): an uncapped diff (a
	// real 90-line file write observed live) pushes the "approval
	// needed" label, the amber rule and the question clean off the top,
	// leaving only the options visible — the opposite of what an
	// approval prompt is for. 12 is chosen the same way
	// bridge.go's collapsedResultLines=3 caps a committed tool result:
	// short enough that the fixed rows around it (header, dashed rules,
	// three options — one of which wraps to two lines — the hint and
	// the closing amber rule, ~13 rows) plus 12 hunk rows comfortably
	// fit inside even a minimal 24-row terminal, while still showing
	// more actual diff than the 3-line collapsed case, since this is
	// the one screen where the user is deciding whether to approve.
	const maxEditPromptDiffRows = 12
	if len(hunkLines) > maxEditPromptDiffRows {
		hidden := len(hunkLines) - maxEditPromptDiffRows
		hunkLines = hunkLines[:maxEditPromptDiffRows]
		hunkLines = append(hunkLines, " "+Muted(fmt.Sprintf("… +%d more lines", hidden)))
	}
	lines = append(lines, hunkLines...)
	lines = append(lines, dashedRule)

	if feedbackMode {
		lines = append(lines,
			" "+Muted("What should be done instead?"),
			fmt.Sprintf(" %s %s%s", KilnAmber(">"), feedback, Faint("▌")),
			" "+Muted("enter to send · esc to decline without a reason"),
		)
		return lines
	}

	opts := []string{
		"Yes",
		"Yes, and switch to accept edits (auto-approve file edits and common file commands) for this\n      session (shift+tab)",
		"No",
	}
	for i, opt := range opts {
		parts := strings.SplitN(opt, "\n", 2)
		key := fmt.Sprintf("%d", i+1)
		lines = append(lines, " "+permissionOptionRow(key, parts[0], i == selected, maxInt(width-1, 1)))
		if len(parts) == 2 {
			if i == selected {
				lines = append(lines, OnRaise(padTo(parts[1], maxInt(width, 1))))
			} else {
				lines = append(lines, Muted(parts[1]))
			}
		}
	}

	lines = append(lines, "", " "+Muted("↑↓ select · enter confirm · esc decline · tab to amend"), amberRule)
	return lines
}

// diffHunksFromEditFile builds DiffHunk rows for an Edit call from the
// file itself: each edit's oldText is located in the file and the whole
// lines it spans become the "-" rows, numbered by their real line, with
// the replacement's lines as the "+" rows (permission-edit.txt rows 20-21:
// "1 -function add(a,b){ return a - b }" / "1 +function add(a,b){ return
// a + b }" for an edit that only touched "a - b"). Falls back to
// diffHunksFromEditArgs when the file cannot be read or an edit does not
// match.
func diffHunksFromEditFile(cwd string, args map[string]any) []DiffHunk {
	path, _ := args["path"].(string)
	if path == "" {
		path, _ = args["file_path"].(string)
	}
	if path == "" {
		return diffHunksFromEditArgs(args)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return diffHunksFromEditArgs(args)
	}
	content := string(data)
	var hunks []DiffHunk
	for _, e := range parseEdits(args["edits"]) {
		idx := strings.Index(content, e.OldText)
		if idx < 0 || e.OldText == "" {
			return diffHunksFromEditArgs(args)
		}
		lineStart := strings.LastIndexByte(content[:idx], '\n') + 1
		end := idx + len(e.OldText)
		lineEnd := strings.IndexByte(content[end:], '\n')
		if lineEnd < 0 {
			lineEnd = len(content)
		} else {
			lineEnd += end
		}
		firstNum := strings.Count(content[:lineStart], "\n") + 1
		oldLines := strings.Split(content[lineStart:lineEnd], "\n")
		newLines := strings.Split(content[lineStart:idx]+e.NewText+content[end:lineEnd], "\n")
		n := len(oldLines)
		if len(newLines) > n {
			n = len(newLines)
		}
		for i := 0; i < n; i++ {
			h := DiffHunk{LineNum: firstNum + i}
			if i < len(oldLines) {
				h.Old = oldLines[i]
			} else {
				h.OldAbsent = true
			}
			if i < len(newLines) {
				h.New = newLines[i]
			} else {
				h.NewAbsent = true
			}
			hunks = append(hunks, h)
		}
	}
	return hunks
}

// diffHunksFromEditArgs builds DiffHunk rows from an Edit call's "edits"
// argument, reusing changepreview.go's parseEdits (same package) so both
// renderers agree on how a pending Edit's arguments are shaped. Edit's
// arguments are an exact-string match/replace, not a unified patch, so
// there is no real file line number to show; hunks are numbered
// sequentially from 1, matching the single-hunk-numbered-"1" case the
// reference capture shows. Only the first line of a multi-line
// replacement is shown per hunk, matching permission-edit.txt's one-line
// example — a genuinely multi-line replacement is not covered by any
// reference capture. [chk] beyond the single-line case.
func diffHunksFromEditArgs(args map[string]any) []DiffHunk {
	edits := parseEdits(args["edits"])
	hunks := make([]DiffHunk, 0, len(edits))
	for i, e := range edits {
		hunks = append(hunks, DiffHunk{LineNum: i + 1, Old: firstLine(e.OldText), New: firstLine(e.NewText)})
	}
	return hunks
}

// diffHunksFromWriteArgs builds DiffHunk rows for a Write call: every
// line of the pending content as a "+" row, sequentially numbered,
// including blank lines (OldAbsent is always set — a Write never has an
// old side — but New == "" still renders as a numbered blank "+" row
// rather than being skipped, so a written file's line numbers stay
// continuous; see the DiffHunk doc comment and defect 2). There is no
// reference capture of the Write prompt, so this shape is [chk].
func diffHunksFromWriteArgs(args map[string]any) []DiffHunk {
	content, _ := args["content"].(string)
	// A file's content conventionally ends with a trailing newline; that
	// newline terminates the last line, it does not introduce a phantom
	// extra blank line after it. strings.Split on "a\nb\n" yields ["a",
	// "b", ""] — three elements for a two-line file — which inflated the
	// hidden-line count in the capped "… +N more lines" row by one
	// (finding write-diff-trailing-blank-line-miscounts). Trimming exactly
	// one trailing "\n" first keeps a genuine trailing blank line (content
	// ending "\n\n") intact, since only the file-terminating newline is
	// stripped.
	content = strings.TrimSuffix(content, "\n")
	lines := strings.Split(content, "\n")
	hunks := make([]DiffHunk, 0, len(lines))
	for i, l := range lines {
		hunks = append(hunks, DiffHunk{LineNum: i + 1, New: l, OldAbsent: true})
	}
	return hunks
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// RenderPlanApproval renders a plan awaiting approval using the same
// permission-block anatomy as RenderBashPermissionPrompt/
// RenderEditPermissionPrompt: a label rule, an amber rule top and bottom,
// an amber question row, the numbered options with the selected row on
// the raised background and an amber key (permissionOptionRow, shared
// with the other two prompts), and a single hint row — per
// docs/kiln-design-handoff/Terminal.dc.html:75-80 (the "isPerm" block)
// and qa/reference/permission.png.
//
// Before this, the plan prompt built its own one-off layout: a stray
// full-width grey rule and a doubled blank row above the "plan" label (an
// artifact of whatever committed the preceding block leaving its own
// trailing blank, which this renderer then added another blank on top
// of), a 3-column inner indent, a dashed rule framing the plan body (the
// dashed rule is the edit prompt's diff-hunk framing — a plan body is not
// a diff), no amber top/bottom rules at all, and a hint sentence
// ("shift+tab to approve with this feedback") hanging under option 3
// instead of one hint row at the bottom (finding
// plan-approval-not-perm-block). This function no longer builds any of
// that — it reuses the exact same label-rule/amber-rule/option-row
// pieces the bash and edit prompts already use, at the same 1-column
// indent, so all three permission variants share one visual language.
//
// Text that intentionally differs from Claude Code's own wording ("Here
// is the plan:" not "Here is Claude's plan:", "the model" not "Claude",
// the plan path under ~/.harness/plans) follows this task's own
// deliverable 7 spec rather than any captured Claude Code wording.
//
// height, when > 0, bounds the plan body's vertical scroll: rows beyond
// the budget are clipped and the last visible one gets a trailing "↓".
//
// selected picks which of the three options carries the raised background
// and amber key. PromptState does not currently support arrow-navigating
// this menu before committing (1/2/3 commit immediately, per
// handlePlanKey), so its caller always passes 0 today; selected=2 is
// exercised only by the unit test, not by any live key path — see the
// handback report.
func RenderPlanApproval(plan, planPath string, width, height, selected int, feedbackMode bool, feedback string) []string {
	amberRule := KilnAmber(strings.Repeat(RuleFillChar(), maxInt(width, 1)))

	lines := []string{
		labelRule("plan", KilnAmber, "", width),
		amberRule,
		" " + KilnAmber(Bold("Ready to code?")),
		"",
		" " + Muted("Here is the plan:"),
	}

	wrapWidth := width - 2
	if wrapWidth < 10 {
		wrapWidth = 10
	}
	var planRows []string
	for _, raw := range strings.Split(plan, "\n") {
		if raw == "" {
			planRows = append(planRows, "")
			continue
		}
		for _, wl := range wrapHard(raw, wrapWidth) {
			planRows = append(planRows, " "+Ink(wl))
		}
	}

	visible := planRows
	scrolled := false
	if height > 0 {
		fixed := len(lines) + 1 /*blank before question*/ + 2 /*question+blank*/ + 3 /*options*/ + 2 /*hint+closing rule*/ + 1 /*path*/
		budget := height - fixed
		if budget < 1 {
			budget = 1
		}
		if len(planRows) > budget {
			visible = planRows[:budget]
			scrolled = true
		}
	}
	if scrolled && len(visible) > 0 {
		last := visible[len(visible)-1]
		gap := (width - 2) - VisibleWidth(last)
		if gap < 1 {
			gap = 1
		}
		visible = append([]string(nil), visible...)
		visible[len(visible)-1] = last + strings.Repeat(" ", gap) + "↓"
	}
	lines = append(lines, visible...)
	lines = append(lines, "")

	if feedbackMode {
		lines = append(lines,
			" "+Muted("What should change about the plan?"),
			fmt.Sprintf(" %s %s%s", KilnAmber(">"), feedback, Faint("▌")),
			" "+Muted("enter to send · esc to go back"),
			amberRule,
		)
		return FitLines(lines, width, " ")
	}

	for _, wl := range wrapHard("kiln has written up a plan and is ready to execute. Would you like to proceed?", wrapWidth+1) {
		lines = append(lines, " "+Muted(wl))
	}
	lines = append(lines, "")

	opts := []string{
		"Yes, and use auto mode",
		"Yes, manually approve edits",
		"Tell kiln what to change",
	}
	for i, opt := range opts {
		key := fmt.Sprintf("%d", i+1)
		lines = append(lines, " "+permissionOptionRow(key, opt, i == selected, maxInt(width-1, 1)))
	}

	lines = append(lines, "", " "+Muted("↑↓ select · enter confirm · shift+tab to tell kiln what to change"), amberRule)

	if planPath != "" {
		lines = append(lines, "")
		for _, wl := range wrapHard(planPath, wrapWidth+1) {
			lines = append(lines, " "+Faint(wl))
		}
	}
	return FitLines(lines, width, " ")
}

// wrapHard word-wraps like wrapPlain, then splits any word longer than
// limit (a URL, a path) so no row exceeds it.
func wrapHard(s string, limit int) []string {
	if limit < 1 {
		limit = 1
	}
	var out []string
	for _, row := range wrapPlain(s, limit) {
		r := []rune(row)
		for len(r) > limit {
			out = append(out, string(r[:limit]))
			r = r[limit:]
		}
		out = append(out, string(r))
	}
	return out
}

// raisedCommand renders the "$ <command>" box on the raise surface across
// width columns. Each span carries the background itself: wrapping
// already-coloured text in OnRaise loses the background at the first
// colour reset, which left only "$ " raised.
func raisedCommand(cmd string, width int) string {
	plain := padTo("$ "+cmd, width)
	if !IsColorEnabled() || VisibleWidth(plain) < 2 {
		return plain
	}
	tx := CurrentTextHex()
	return onRaiseSpan(tx.Dim, plain[:2]) + onRaiseSpan(tx.Ink, plain[2:])
}
