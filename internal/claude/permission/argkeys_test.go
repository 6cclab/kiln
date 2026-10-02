package permission

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/claude/settings"
)

// An MCP tool's input goes to a server that may read any path-valued key,
// so the workspace check must hold for every one of them, not only the
// first PathArgOf finds. Before, {"path": inside, "file_path": outside}
// was judged on "path" alone and allowed.
func TestGateChecksEveryPathKeyForTheWorkspaceBoundary(t *testing.T) {
	g := NewGate(GateOptions{
		Permissions: settings.Permissions{Allow: []string{"mcp__fs__write_file"}},
		Mode:        settings.ModeAuto,
		Roots:       []string{work(t)},
	})
	inside := filepath.Join(work(t), "ok.txt")
	outside := filepath.Join(t.TempDir(), "zshrc")
	args := map[string]any{"path": inside, "file_path": outside}
	primary, _ := PrimaryArgOf(args)
	blocked, err := g.Check(context.Background(), Request{ToolName: "mcp__fs__write_file", PrimaryArg: primary, Args: args})
	if err != nil {
		t.Fatal(err)
	}
	if blocked == nil || !strings.Contains(blocked.Reason, outside) {
		t.Fatalf("blocked = %+v, want a refusal naming %s as outside the workspace", blocked, outside)
	}

	// Every path inside: no question.
	args = map[string]any{"path": inside, "file_path": inside}
	if blocked, err := g.Check(context.Background(), Request{ToolName: "mcp__fs__write_file", PrimaryArg: inside, Args: args}); err != nil || blocked != nil {
		t.Fatalf("Check = %+v, %v; want allowed", blocked, err)
	}
}
