package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/claude/permission"
	"github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/msg"
)

// TestDispatchRefusesCaseVariantKeysBeforeTheGate drives a subagent (its
// own Harness, gated by the shared Gate in acceptEdits mode, where a write
// inside the workspace runs without asking) through write calls that name
// a path outside the workspace with a case-variant key. The gate reads
// "path" exactly; the write tool decodes keys case-insensitively. Without
// the turn loop's tool.CheckArgs refusal, "PATH" alone wrote outside the
// workspace: the gate saw no path, so nothing was outside.
func TestDispatchRefusesCaseVariantKeysBeforeTheGate(t *testing.T) {
	cases := []struct {
		name string
		args string // YAML flow mapping; OUTSIDE is replaced by the path
	}{
		{"decoy path plus upper-case PATH", `{path: ok.txt, PATH: "OUTSIDE", content: pwned}`},
		{"upper-case PATH only", `{PATH: "OUTSIDE", content: pwned}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outside := filepath.Join(t.TempDir(), "zshrc")
			script := "model: faux-1\nsteps:\n" +
				"  - tool_call: {name: write, args: " + strings.ReplaceAll(tc.args, "OUTSIDE", outside) + ", id: tc1}\n" +
				"  - on_tool_result: tc1\n" +
				"    then:\n" +
				"      - text: \"done\"\n"

			gate := permission.NewGate(permission.GateOptions{Mode: settings.ModeAcceptEdits})
			d, _, cwd := newParentAndDispatcher(t, script, gate)
			gate.AddRoot(cwd)

			var results []*msg.ToolResultMessage
			d.OnEvent = func(ev SubagentEvent) {
				if ev.Kind == SubagentEventTool && ev.ToolResult != nil {
					results = append(results, ev.ToolResult)
				}
			}

			if _, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Description: "x", Prompt: "go"}); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}

			if _, err := os.Stat(outside); !os.IsNotExist(err) {
				t.Fatalf("%s exists (stat err %v): a case-variant key reached the write tool", outside, err)
			}
			if _, err := os.Stat(filepath.Join(cwd, "ok.txt")); !os.IsNotExist(err) {
				t.Fatalf("ok.txt exists (stat err %v): the call should have been refused outright", err)
			}
			if len(results) != 1 {
				t.Fatalf("got %d tool results, want 1", len(results))
			}
			text := msg.TextOf(results[0].Content)
			if !results[0].IsError || !strings.Contains(text, "did not run") || !strings.Contains(text, "only in case") {
				t.Fatalf("tool result = %q (isError %v), want the case-variant refusal", text, results[0].IsError)
			}
			if blocked := gate.Blocked(); len(blocked) != 0 {
				t.Fatalf("gate.Blocked() = %v: the gate ran, but the refusal must come before it", blocked)
			}
		})
	}
}
