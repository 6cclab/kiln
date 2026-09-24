package tui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Transcript rendering — the layout defined in docs/claude-code-parity.md
// §4a, ported from transcript.ts.
//
// Pure functions from data to lines. Nothing here touches a terminal, which
// is what lets the layout be asserted in tests rather than eyeballed, and
// is where parity is actually won or lost.
//
// The rules that matter, each of which is easy to get subtly wrong:
//
//   - Tool calls get a flush-left marker. Assistant prose gets none.
//     Marking both makes the transcript unreadable.
//   - Results indent two spaces, then the glyph, then two more. The glyph
//     appears on the FIRST result line only; continuation lines align
//     under the content, not under the glyph.
//   - A call line names one identifying argument, not a serialized object.
//   - Results are summarized, never dumped.

// Two spaces, glyph, two spaces -> content starts at column 5.
const resultIndent = "  "
const continuationIndent = "     "

// CallStatus is the state of a tool call, for marker colour.
type CallStatus string

const (
	CallRunning CallStatus = "running"
	CallOK      CallStatus = "ok"
	CallError   CallStatus = "error"
)

// ToolCallView is the data renderToolCall needs.
type ToolCallView struct {
	Name string
	// PrimaryArg is the one identifying argument, already stringified.
	PrimaryArg string
	Status     CallStatus
	// ResultLines are summary line(s), already truncated by the caller to
	// the tier's budget.
	ResultLines []string
	// TotalLines is the total lines available, when more exist than are
	// shown. Zero means "not set" (there is no ambiguity: a call with a
	// result always has at least one line).
	TotalLines int
	// HasTotalLines distinguishes "0 total lines" (never happens in
	// practice) from "not tracked", matching the TS `number | undefined`.
	HasTotalLines bool
}

func markerColor(status CallStatus) func(string) string {
	switch status {
	case CallError:
		return Red
	case CallRunning:
		return Dim
	default:
		return Green
	}
}

// RenderToolCall renders a tool call and its result:
//
//	⏺ Read(src/provider/ollama.ts)
//	  ⎿  Read 240 lines (ctrl+r to expand)
func RenderToolCall(view ToolCallView) []string {
	gl := G()
	head := fmt.Sprintf("%s %s(%s)", markerColor(view.Status)(gl.Call), Bold(view.Name), Dim(view.PrimaryArg))
	lines := []string{head}

	body := view.ResultLines
	if len(body) == 0 {
		return lines
	}

	// Glyph on the first result line only; the rest align under its
	// content.
	lines = append(lines, fmt.Sprintf("%s%s  %s", resultIndent, Gray(gl.Result), body[0]))
	for _, extra := range body[1:] {
		lines = append(lines, continuationIndent+extra)
	}

	if view.HasTotalLines && view.TotalLines > len(body) {
		lines = append(lines, continuationIndent+Dim(fmt.Sprintf("… +%d lines (ctrl+r to expand)", view.TotalLines-len(body))))
	}
	return lines
}

// TodoStatus is a todo item's completion state.
type TodoStatus string

const (
	TodoPendingStatus    TodoStatus = "pending"
	TodoInProgressStatus TodoStatus = "in_progress"
	TodoCompletedStatus  TodoStatus = "completed"
)

// TodoView is one todo list entry.
type TodoView struct {
	Content string
	Status  TodoStatus
}

// RenderTodos renders a todo list under a tool-call header. Completed
// items are struck through and dimmed; the in-progress item is distinct
// from both, because "which one is happening now" is the only question the
// list has to answer at a glance.
func RenderTodos(todos []TodoView) []string {
	gl := G()
	lines := []string{fmt.Sprintf("%s %s", Green(gl.Call), Bold("Update Todos"))}

	for i, todo := range todos {
		prefix := continuationIndent
		if i == 0 {
			prefix = fmt.Sprintf("%s%s  ", resultIndent, Gray(gl.Result))
		}
		switch todo.Status {
		case TodoCompletedStatus:
			lines = append(lines, fmt.Sprintf("%s%s %s", prefix, Dim(gl.TodoDone), Dim(Strike(todo.Content))))
		case TodoInProgressStatus:
			lines = append(lines, fmt.Sprintf("%s%s %s", prefix, gl.TodoActive, Bold(todo.Content)))
		default:
			lines = append(lines, fmt.Sprintf("%s%s %s", prefix, gl.TodoPending, todo.Content))
		}
	}
	return lines
}

var hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// RenderDiff renders a unified diff.
//
// Line numbers come from the new file for additions and the old file for
// deletions, so a reader can navigate to what they are looking at.
// Colouring the whole line rather than just the marker is what makes a
// diff scannable at speed.
func RenderDiff(patch string, startLine int) []string {
	if startLine == 0 {
		startLine = 1
	}
	out := []string{}
	oldNo := startLine
	newNo := startLine

	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "@@") {
			// Re-anchor on a hunk header so line numbers stay truthful
			// across gaps.
			if m := hunkHeaderRe.FindStringSubmatch(line); m != nil {
				oldNo, _ = strconv.Atoi(m[1])
				newNo, _ = strconv.Atoi(m[2])
			}
			out = append(out, Dim(line))
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"):
			out = append(out, Green(fmt.Sprintf("%5d + %s", newNo, line[1:])))
			newNo++
		case strings.HasPrefix(line, "-"):
			out = append(out, Red(fmt.Sprintf("%5d - %s", oldNo, line[1:])))
			oldNo++
		default:
			rest := line
			if len(rest) > 0 {
				rest = rest[1:]
			}
			out = append(out, Dim(fmt.Sprintf("%5d   %s", newNo, rest)))
			oldNo++
			newNo++
		}
	}
	return out
}

// SpinnerArgs is the data RenderSpinner needs.
type SpinnerArgs struct {
	Frame          int
	Label          string
	ElapsedSeconds int
	// Tokens is nil when there is no live token count yet.
	Tokens *int
}

// RenderSpinner renders the working indicator:
//
//	✢ working (3s · ↓ 4.2k tokens)
//
// On a local model a turn runs past two minutes, so this is load-bearing
// rather than decorative — a still screen reads as a hang. The token count
// must come from the live stream, not from the final usage record.
func RenderSpinner(args SpinnerArgs) string {
	gl := G()
	spin := gl.Spinner[args.Frame%len(gl.Spinner)]
	parts := []string{fmt.Sprintf("%ds", args.ElapsedSeconds)}
	if args.Tokens != nil {
		parts = append(parts, fmt.Sprintf("↓ %s tokens", FormatTokens(*args.Tokens)))
	}
	return fmt.Sprintf("%s %s %s", Green(spin), strings.ToLower(args.Label), Dim(fmt.Sprintf("(%s)", strings.Join(parts, " · "))))
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

// Labels are gerunds for the spinner.
//
// Deliberately this project's own vocabulary rather than Claude Code's
// word list: the *shape* is the parity requirement, the specific words are
// flavor, and copying someone's jokes is not parity.
var Labels = []string{
	"Thinking",
	"Working",
	"Pondering",
	"Chewing",
	"Considering",
	"Noodling",
	"Mulling",
	"Digging",
	"Untangling",
	"Reckoning",
}

// PickLabel picks a gerund deterministically from a seed.
func PickLabel(seed int) string {
	n := seed % len(Labels)
	if n < 0 {
		n += len(Labels)
	}
	return Labels[n]
}

// RenderError renders an error message: red, visually distinct from
// ordinary tool output.
func RenderError(message string) []string {
	lines := strings.Split(message, "\n")
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = Red(line)
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

// RenderThinking renders a reasoning block:
//
//	∴ Thinking (ctrl+r to expand)
//
// Collapsed by default, per the parity spec. That is not only a visual
// preference: the models this harness targets are reasoning models, and a
// small local model emits a block of reasoning before most answers. Shown
// in full it buries the answer under its own working, every turn.
//
// The label stays present tense ("Thinking") in both states, matching
// Claude Code's "∴ Thinking" / "∴ Thinking…"; the expand hint appears only
// once the block is stable, when there is something to expand.
//
// Dimmed italic when expanded, so it never competes with the assistant's
// actual prose — the reader should be able to skip it without deciding to.
func RenderThinking(view ThinkingView) []string {
	gl := G()
	body := strings.TrimSpace(view.Text)
	if body == "" {
		return []string{}
	}

	lines := strings.Split(body, "\n")
	// Present tense either way, like Claude Code: "Thinking…" while
	// streaming (nothing stable to expand yet), "Thinking" once it has
	// finished.
	label := "Thinking"
	if view.Active {
		label = "Thinking…"
	}

	if !view.Expanded {
		hint := ""
		if !view.Active {
			hint = " " + Dim("(ctrl+r to expand)")
		}
		return []string{fmt.Sprintf("%s %s%s", Dim(gl.Thinking), Dim(label), hint)}
	}

	out := []string{fmt.Sprintf("%s %s", Dim(gl.Thinking), Dim(label))}
	for _, line := range lines {
		out = append(out, resultIndent+Dim(Italic(line)))
	}
	return out
}

// RenderUserMessage renders the user's own message: a subtle "❯" pointer,
// then the text, plain. No fill band — the pointer is what marks the line,
// and the assistant's prose already sits flush at the left margin so the
// two read as different voices without fighting.
//
// The pointer appears on the first visual line only; wrapped continuation
// lines carry the text alone.
func RenderUserMessage(text string, width int) []string {
	gl := G()
	inner := width - 2
	if inner < 1 {
		inner = 1
	}
	var out []string
	for i, line := range strings.Split(text, "\n") {
		wrapped := splitLines(ansiWrap(line, inner))
		for j, wl := range wrapped {
			if i == 0 && j == 0 {
				out = append(out, fmt.Sprintf("%s %s", Dim(gl.UserMark), wl))
			} else {
				out = append(out, wl)
			}
		}
	}
	return out
}

// TurnSummary is the data RenderTurnSummary needs.
type TurnSummary struct {
	Seconds int
	// Tokens and ToolCalls are 0 when there is nothing to say about them,
	// matching the TS `number | undefined`'s falsy-omit behavior (a turn
	// that used exactly zero tokens or zero tool calls has nothing to
	// report either way).
	Tokens    int
	ToolCalls int
}

// RenderTurnSummary renders the line that closes a turn:
//
//	✳ Worked for 32s · 1 tool call · 4.2k tokens
//
// Without it a finished turn just stops, and the transcript gives no sense
// of what a request cost. Elapsed time is the honest headline; tokens and
// tool calls explain it.
func RenderTurnSummary(summary TurnSummary) []string {
	gl := G()
	parts := []string{fmt.Sprintf("%ds", summary.Seconds)}
	if summary.ToolCalls != 0 {
		plural := "s"
		if summary.ToolCalls == 1 {
			plural = ""
		}
		parts = append(parts, fmt.Sprintf("%d tool call%s", summary.ToolCalls, plural))
	}
	if summary.Tokens != 0 {
		parts = append(parts, fmt.Sprintf("%s tokens", FormatTokens(summary.Tokens)))
	}
	return []string{Dim(fmt.Sprintf("%s Worked for %s", gl.Summary, strings.Join(parts, " · ")))}
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
