package tui

import (
	"strings"
	"testing"
)

// A command that runs outside the OS sandbox says so in its approval
// prompt, bash's and the generic one alike; a sandboxed one does not.
func TestPermissionPromptUnsandboxedNote(t *testing.T) {
	plain := func(lines []string) string { return stripANSI(strings.Join(lines, "\n")) }

	got := plain(RenderBashPermissionPrompt(BashPermissionRequest{Command: "docker compose up", Unsandboxed: true}, 80, 0))
	if !strings.Contains(got, "runs outside the sandbox") {
		t.Errorf("bash prompt lacks the unsandboxed note:\n%s", got)
	}
	got = plain(RenderBashPermissionPrompt(BashPermissionRequest{Command: "docker compose up"}, 80, 0))
	if strings.Contains(got, "sandbox") {
		t.Errorf("sandboxed bash prompt mentions the sandbox:\n%s", got)
	}
	got = plain(RenderPermissionPrompt(PermissionRequest{ToolName: "bash_background", PrimaryArg: "docker compose up", Unsandboxed: true}, "/", 80, 0, false, ""))
	if !strings.Contains(got, "runs outside the sandbox") {
		t.Errorf("generic prompt lacks the unsandboxed note:\n%s", got)
	}
}
