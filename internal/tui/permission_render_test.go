package tui

import (
	"strings"
	"testing"
)

func TestSummarizeArgRelativizesPath(t *testing.T) {
	req := PermissionRequest{ToolName: "read", PrimaryArg: "/work/project/src/foo.go"}
	got := SummarizeArg(req, "/work/project")
	if got != "src/foo.go" {
		t.Errorf("got %q, want %q", got, "src/foo.go")
	}
}

func TestSummarizeArgKeepsAbsoluteOutsideCwd(t *testing.T) {
	req := PermissionRequest{ToolName: "read", PrimaryArg: "/tmp/elsewhere/foo.go"}
	got := SummarizeArg(req, "/work/project")
	if got != "/tmp/elsewhere/foo.go" {
		t.Errorf("a path outside the workspace should stay absolute rather than becoming ../../..., got %q", got)
	}
}

func TestSummarizeArgTruncatesLongArgs(t *testing.T) {
	long := make([]byte, 250)
	for i := range long {
		long[i] = 'x'
	}
	req := PermissionRequest{ToolName: "bash", PrimaryArg: string(long)}
	got := SummarizeArg(req, "/")
	if len(got) >= 250 {
		t.Errorf("expected truncation, got length %d", len(got))
	}
	if got[len(got)-1] != ')' {
		t.Errorf("expected char-count suffix, got %q", got)
	}
}

func TestRenderPermissionPromptMenuText(t *testing.T) {
	req := PermissionRequest{ToolName: "Bash", PrimaryArg: "rm -rf /tmp/x"}
	out := RenderPermissionPrompt(req, "/", 80, 0, false, "")
	full := strings.Join(out, "\n")
	for _, want := range []string{"Yes", "Yes, and don't ask again for this", "No, and tell kiln what to do instead", "1-3, y/n, or esc to decline"} {
		if !strings.Contains(full, want) {
			t.Errorf("menu missing %q in %v", want, out)
		}
	}
}

func TestRenderPermissionPromptFeedbackMode(t *testing.T) {
	req := PermissionRequest{ToolName: "Bash", PrimaryArg: "rm -rf /"}
	out := RenderPermissionPrompt(req, "/", 80, 0, true, "do this instead")
	if !strings.Contains(strings.Join(out, "\n"), "do this instead") {
		t.Errorf("feedback text missing from %v", out)
	}
}

func TestRenderPlanApprovalMenuText(t *testing.T) {
	out := RenderPlanApproval("# Plan\n\n1. Do the thing", "~/.harness/plans/x.md", 80, 0, 0, false, "")
	full := strings.Join(out, "\n")
	for _, want := range []string{"Yes, and use auto mode", "Yes, manually approve edits", "Tell kiln what to change"} {
		if !strings.Contains(full, want) {
			t.Errorf("plan menu missing %q", want)
		}
	}
}

// TestRenderBashPermissionPrompt_MatchesReference pins the Bash
// permission prompt's rows to the kiln design (docs/kiln-design.md's
// "perm" block anatomy): a "approval needed" label rule, an amber-rule
// framed body with "Allow kiln to run this command?", the command on a
// raised background, the description, four numbered options (kiln has no
// "❯" marker on permission options — selection is the raised background
// plus an amber key, both invisible with colour disabled) and the hint
// row. Colour is disabled so this pins content and column layout, not
// styling.
func TestRenderBashPermissionPrompt_MatchesReference(t *testing.T) {
	SetColorEnabled(false)
	defer SetColorEnabled(true)

	req := BashPermissionRequest{
		Command:     "openssl rand -hex 4",
		Description: "Generate 4 random hex bytes",
	}
	got := RenderBashPermissionPrompt(req, 100, 0)

	want := []string{
		"approval needed " + strings.Repeat("─", 84),
		strings.Repeat("─", 100),
		" Allow kiln to run this command?",
		"",
		" $ openssl rand -hex 4",
		"   Generate 4 random hex bytes",
		"",
		strings.Repeat("─", 100),
		" 1  Yes",
		" 2  Yes, and don\u2019t ask again for: openssl rand *",
		" 3  Yes, and switch to auto mode \u00b7 auto mode handles these prompts for you",
		" 4  No",
		"",
		" \u2191\u2193 select \u00b7 enter confirm \u00b7 esc decline \u00b7 tab to amend",
	}

	if len(got) != len(want) {
		t.Fatalf("row count = %d, want %d\ngot:  %q\nwant: %q", len(got), len(want), got, want)
	}
	for i := range want {
		row := got[i]
		if i == 4 { // the "$ ..." row carries the raised background's padding
			row = strings.TrimRight(row, " ")
		}
		if row != want[i] {
			t.Errorf("row %d:\n got:  %q\n want: %q", i, row, want[i])
		}
	}
}

// TestRenderBashPermissionPrompt_NoDescription checks the description row
// is omitted when the call has none, per the contract's explicit rule.
func TestRenderBashPermissionPrompt_NoDescription(t *testing.T) {
	SetColorEnabled(false)
	defer SetColorEnabled(true)

	req := BashPermissionRequest{Command: "ls -la"}
	got := RenderBashPermissionPrompt(req, 100, 0)
	for _, l := range got {
		if strings.TrimRight(l, " ") == "  " {
			t.Errorf("expected no blank description row, got one: %q", got)
		}
	}
	if strings.TrimRight(got[4], " ") != " $ ls -la" {
		t.Fatalf("row 4 = %q, want command row", got[4])
	}
	if got[5] != "" {
		t.Errorf("row 5 should be the blank row after the command (no description), got %q", got[5])
	}
}

// TestBashDontAskRule checks the "first two words + *" gate expression.
func TestBashDontAskRule(t *testing.T) {
	cases := map[string]string{
		"openssl rand -hex 4": "openssl rand *",
		"ls":                  "ls *",
		"":                    "*",
		"git commit -m x":     "git commit *",
	}
	for cmd, want := range cases {
		if got := bashDontAskRule(cmd); got != want {
			t.Errorf("bashDontAskRule(%q) = %q, want %q", cmd, got, want)
		}
	}
}

func TestRenderBashPermissionPrompt_Widths(t *testing.T) {
	SetColorEnabled(false)
	defer SetColorEnabled(true)
	for _, width := range []int{100, 60} {
		req := BashPermissionRequest{Command: "echo hi", Description: "say hi"}
		rows := RenderBashPermissionPrompt(req, width, 2)
		if len(rows[0]) != 0 && VisibleWidth(rows[0]) != width {
			t.Errorf("width %d: rule row width = %d", width, VisibleWidth(rows[0]))
		}
	}
}

// TestRenderEditPermissionPrompt_MatchesReference pins the Edit
// permission prompt's rows to the kiln design: a "approval needed" label
// rule, an amber-rule framed "Allow kiln to edit <path>?" header, a
// dashed rule around the diff hunk rows, and the numbered options with no
// "❯" marker (kiln marks selection with the raised background and an
// amber key, invisible with colour disabled). Colour is disabled so this
// pins content and column layout, not styling.
func TestRenderEditPermissionPrompt_MatchesReference(t *testing.T) {
	SetColorEnabled(false)
	defer SetColorEnabled(true)

	req := EditPermissionRequest{
		Kind: EditKindEdit,
		Path: "math.js",
		Hunks: []DiffHunk{
			{LineNum: 1, Old: "function add(a,b){ return a - b }", New: "function add(a,b){ return a + b }"},
		},
	}
	got := RenderEditPermissionPrompt(req, 100, 0, false, "")

	want := []string{
		"approval needed " + strings.Repeat("\u2500", 84),
		strings.Repeat("\u2500", 100),
		" Allow kiln to edit math.js?",
		strings.Repeat("\u254c", 100),
		" 1 -function add(a,b){ return a - b }",
		" 1 +function add(a,b){ return a + b }",
		strings.Repeat("\u254c", 100),
		" 1  Yes",
		" 2  Yes, and switch to accept edits (auto-approve file edits and common file commands) for this",
		"      session (shift+tab)",
		" 3  No",
		"",
		" \u2191\u2193 select \u00b7 enter confirm \u00b7 esc decline \u00b7 tab to amend",
	}

	if len(got) != len(want) {
		t.Fatalf("row count = %d, want %d\ngot:  %q\nwant: %q", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d:\n got:  %q\n want: %q", i, got[i], want[i])
		}
	}
}

func TestRenderEditPermissionPrompt_WriteKindUsesWriteHeader(t *testing.T) {
	SetColorEnabled(false)
	defer SetColorEnabled(true)

	req := EditPermissionRequest{Kind: EditKindWrite, Path: "new.txt", Hunks: []DiffHunk{{LineNum: 1, New: "hello"}}}
	got := RenderEditPermissionPrompt(req, 100, 0, false, "")
	if got[2] != " Allow kiln to write to new.txt?" {
		t.Errorf("row 2 = %q, want the write-header row", got[2])
	}
	if got[4] != " 1 +hello" {
		t.Errorf("row 4 = %q, want the +hello row (Write has no old line)", got[4])
	}
}

func TestRenderEditPermissionPrompt_NumberWidthFromDiff(t *testing.T) {
	SetColorEnabled(false)
	defer SetColorEnabled(true)

	req := EditPermissionRequest{
		Kind: EditKindEdit,
		Path: "f.go",
		Hunks: []DiffHunk{
			{LineNum: 1, Old: "a", New: "a2"},
			{LineNum: 12, Old: "b", New: "b2"},
		},
	}
	got := RenderEditPermissionPrompt(req, 100, 0, false, "")
	// Number column width must come from the widest line number (12, two
	// digits), so the single-digit "1" row pads to match.
	if got[4] != "  1 -a" {
		t.Errorf("row 4 = %q, want padded to two-digit width", got[4])
	}
	if got[6] != " 12 -b" {
		t.Errorf("row 6 = %q", got[6])
	}
}

// TestRenderPlanApproval_MatchesReference pins the plan-approval
// prompt's structural rows to the kiln design (docs/kiln-design.md's
// "plan" block anatomy and the perm block's option layout): a "plan"
// label rule, "Ready to code?", the plan body between dashed/thin rules,
// the proceed question ("kiln has written up a plan...", not "the
// model..." — see the real bug noted in the handback), and the three
// numbered options (no "❯" marker; kiln marks selection with the raised
// background and an amber key, invisible with colour disabled).
func TestRenderPlanApproval_MatchesReference(t *testing.T) {
	SetColorEnabled(false)
	defer SetColorEnabled(true)

	plan := "Rename math.js \u2192 calc.js\n\nContext\n\n" +
		"The repo contains a single source file, math.js (34 bytes, one add function). The user wants it renamed to calc.js. A grep across the working tree found no other file referencing math, so this is a self-contained rename with no import sites to update.\n\n" +
		"Note: math.js currently has uncommitted modifications (git status shows  M math.js). Using git mv preserves that staged/unstaged content and records the rename.\n\n" +
		"Step 1 \u2014 Rename the file\n\ngit mv math.js calc.js\n\n" +
		"Critical file: math.js \u2192 calc.js. Contents unchanged:\n\nfunction add(a,b){ return a + b }\n\n" +
		"Step 2 \u2014 Verify no dangling references"

	got := RenderPlanApproval(plan, "~/.harness/plans/3make-a-two-step-plan-ticklish-aurora.md", 100, 0, 0, false, "")

	wantPrefix := []string{
		"plan " + strings.Repeat("\u2500", 95),
		"   Ready to code?",
		"",
		"   Here is the plan:",
		"  " + strings.Repeat("\u254c", 96),
		"   Rename math.js \u2192 calc.js",
		"",
		"   Context",
		"",
	}
	for i, w := range wantPrefix {
		if got[i] != w {
			t.Errorf("row %d:\n got:  %q\n want: %q", i, got[i], w)
		}
	}

	// The closing rule, question, options and path row are the
	// structural tail — find them relative to the end since the wrapped
	// plan body's exact row count is a wrapping-formula detail already
	// flagged [chk] in dialog.go.
	tail := got[len(got)-9:]
	wantTail := []string{
		"  " + strings.Repeat("\u2500", 96),
		"   kiln has written up a plan and is ready to execute. Would you like to proceed?",
		"",
		"   1  Yes, and use auto mode",
		"   2  Yes, manually approve edits",
		"   3  Tell kiln what to change",
		"        shift+tab to approve with this feedback",
		"",
		"   ~/.harness/plans/3make-a-two-step-plan-ticklish-aurora.md",
	}
	for i, w := range wantTail {
		if tail[i] != w {
			t.Errorf("tail row %d:\n got:  %q\n want: %q", i, tail[i], w)
		}
	}
}

// TestRenderPlanApproval_SelectedThird checks option 3's row carries the
// selected styling (raised background + amber key) that the other two
// options do not — with colour enabled, since kiln does not mark
// selection with a "❯" glyph on this menu (docs/kiln-design.md: selected
// row background #241f18, amber key; other rows dim text, key #7d7262),
// so a plain-text (colour-disabled) diff cannot distinguish selected from
// unselected rows here.
func TestRenderPlanApproval_SelectedThird(t *testing.T) {
	SetColorEnabled(true)
	defer SetColorEnabled(false)

	got := RenderPlanApproval("Body", "~/.harness/plans/x.md", 100, 0, 2, false, "")
	full := strings.Join(got, "\n")
	if !strings.Contains(full, "Tell kiln what to change") {
		t.Fatalf("expected option 3's text, got %v", got)
	}
	var row3, row1 string
	for _, l := range got {
		if strings.Contains(l, "Tell kiln what to change") {
			row3 = l
		}
		if strings.Contains(l, "Yes, and use auto mode") {
			row1 = l
		}
	}
	if !strings.Contains(row3, "48;2;36;31;24") { // OnRaise background
		t.Errorf("expected option 3's row to carry the raised background, got %q", row3)
	}
	if strings.Contains(row1, "48;2;36;31;24") {
		t.Errorf("option 1 should not carry the raised background when selected=2, got %q", row1)
	}
}

func TestRenderPlanApproval_ScrollIndicatorWhenClipped(t *testing.T) {
	SetColorEnabled(false)
	defer SetColorEnabled(true)

	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, "line")
	}
	plan := strings.Join(lines, "\n")
	got := RenderPlanApproval(plan, "~/.harness/plans/x.md", 100, 20, 0, false, "")
	found := false
	for _, l := range got {
		if strings.HasSuffix(l, "\u2193") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a scroll indicator when the plan overflows the height budget, got %v", got)
	}
}
