package tools

import (
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/execenv"
)

// The QA run's model began every command with cd "$PWD" &&, which kiln,
// like Claude Code, cannot prove stays in the workspace, so read-only
// commands prompted. The bash tool's description tells the model each
// command already starts in the working directory, and it does.
func TestBashToolSaysNoCdIsNeeded(t *testing.T) {
	dir := t.TempDir()
	bt := BashTool(execenv.New(dir))
	if !strings.Contains(bt.Description, `do not begin one with a cd to it (cd "$PWD" && …)`) {
		t.Errorf("description does not tell the model to skip the cd:\n%s", bt.Description)
	}
	// What the description promises: a fresh command starts there, even
	// after an earlier one changed directory.
	execTool(t, bt, map[string]any{"command": "cd /"})
	if got := strings.TrimSpace(resultText(execTool(t, bt, map[string]any{"command": "pwd -P"}))); !strings.HasSuffix(got, strings.TrimPrefix(dir, "/private")) {
		t.Errorf("second command ran in %q, want %q", got, dir)
	}
}
