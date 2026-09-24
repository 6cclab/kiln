package tui

import (
	"fmt"
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
func RenderPermissionPrompt(req PermissionRequest, cwd string, width int, feedbackMode bool, feedback string) []string {
	lines := []string{
		"",
		fmt.Sprintf("%s %s", Yellow("?"), Bold("Permission required")),
		fmt.Sprintf("  %s %s%s", Gray("│"), Bold(req.ToolName), argSuffix(req, cwd)),
	}

	// The reason for the prompt changes what the answer should be, so say
	// it.
	if req.OutsideWorkspace {
		lines = append(lines, fmt.Sprintf("  %s %s", Gray("│"), Yellow("outside the workspace")))
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
			fmt.Sprintf("  %s", Dim("What should be done instead?")),
			fmt.Sprintf("  %s %s%s", Green(">"), feedback, Gray("▌")),
			fmt.Sprintf("  %s", Dim("enter to send · esc to decline without a reason")),
		)
		return FitLines(lines, width, "    ")
	}

	lines = append(lines,
		fmt.Sprintf("  %s Yes", Green("1.")),
		fmt.Sprintf("  %s Yes, and don't ask again for this", Green("2.")),
		fmt.Sprintf("  %s No, and tell the model what to do instead", Red("3.")),
		"",
		fmt.Sprintf("  %s", Dim("1-3, y/n, or esc to decline")),
	)
	return FitLines(lines, width, "    ")
}

func argSuffix(req PermissionRequest, cwd string) string {
	if req.PrimaryArg == "" {
		return ""
	}
	return fmt.Sprintf("(%s)", Dim(SummarizeArg(req, cwd)))
}

// RenderPlanApproval renders a plan awaiting approval, fitted to width.
//
// The plan is shown in full rather than summarized: approving a plan you
// cannot read is the same failure as approving an edit you cannot see.
func RenderPlanApproval(plan string, width int, feedbackMode bool, feedback string) []string {
	lines := []string{"", fmt.Sprintf("%s %s", Yellow("?"), Bold("Plan ready for approval")), ""}
	for _, line := range strings.Split(plan, "\n") {
		lines = append(lines, "  "+line)
	}
	lines = append(lines, "")

	if feedbackMode {
		lines = append(lines,
			fmt.Sprintf("  %s", Dim("What should change about the plan?")),
			fmt.Sprintf("  %s %s%s", Green(">"), feedback, Gray("▌")),
			fmt.Sprintf("  %s", Dim("enter to send · esc to go back")),
		)
		return FitLines(lines, width, "    ")
	}

	lines = append(lines,
		fmt.Sprintf("  %s Approve and proceed %s", Green("1."), Dim("(auto-accept edits)")),
		fmt.Sprintf("  %s Approve, but confirm each change", Green("2.")),
		fmt.Sprintf("  %s Keep planning, with feedback", Red("3.")),
		"",
		fmt.Sprintf("  %s", Dim("1-3, y/n")),
	)
	return FitLines(lines, width, "    ")
}
