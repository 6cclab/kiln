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
	out := RenderPermissionPrompt(req, "/", 80, false, "")
	full := strings.Join(out, "\n")
	for _, want := range []string{"Yes", "Yes, and don't ask again for this", "No, and tell the model what to do instead", "1-3, y/n, or esc to decline"} {
		if !strings.Contains(full, want) {
			t.Errorf("menu missing %q in %v", want, out)
		}
	}
}

func TestRenderPermissionPromptFeedbackMode(t *testing.T) {
	req := PermissionRequest{ToolName: "Bash", PrimaryArg: "rm -rf /"}
	out := RenderPermissionPrompt(req, "/", 80, true, "do this instead")
	if !strings.Contains(strings.Join(out, "\n"), "do this instead") {
		t.Errorf("feedback text missing from %v", out)
	}
}

func TestRenderPlanApprovalMenuText(t *testing.T) {
	out := RenderPlanApproval("# Plan\n\n1. Do the thing", 80, false, "")
	full := strings.Join(out, "\n")
	for _, want := range []string{"Approve and proceed", "Approve, but confirm each change", "Keep planning, with feedback"} {
		if !strings.Contains(full, want) {
			t.Errorf("plan menu missing %q", want)
		}
	}
}
