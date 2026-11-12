package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// fileCycle drives two before/mutate/observe rounds (A->B, B->A) and returns
// the second observation.
func fileCycle(t *testing.T, g *progressLoopGuard, call msg.ToolCall, write func(string)) progressLoopEvent {
	t.Helper()
	result := msg.ToolResultMessage{Content: msg.Blocks{msg.Text("edited")}}
	var event progressLoopEvent
	for _, content := range []string{"B", "A"} {
		before := g.before(call)
		write(content)
		event = g.observe(call, result, before)
	}
	return event
}

func TestProgressLoopSymlinkInsideWorkdirIsTracked(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.txt")
	if err := os.WriteFile(real, []byte("A"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	g := progressLoopGuard{limit: 2, cwd: dir}
	call := msg.NewToolCall("edit", "edit", map[string]any{"path": "link.txt"})
	event := fileCycle(t, &g, call, func(c string) {
		if err := os.WriteFile(real, []byte(c), 0600); err != nil {
			t.Fatal(err)
		}
	})
	if event.Notice == "" {
		t.Fatalf("revert through an in-tree symlink was not detected: %+v", event)
	}
}

func TestProgressLoopSymlinkLeavingWorkdirStaysUnknown(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	target := filepath.Join(outside, "t.txt")
	if err := os.WriteFile(target, []byte("A"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	g := progressLoopGuard{limit: 2, cwd: dir}
	call := msg.NewToolCall("edit", "edit", map[string]any{"path": "link.txt"})
	if g.before(call).valid {
		t.Fatal("a link leaving the working directory was snapshotted")
	}
	event := fileCycle(t, &g, call, func(c string) { _ = os.WriteFile(target, []byte(c), 0600) })
	if event.Notice != "" || event.Err != nil {
		t.Fatalf("outside link triggered: %+v", event)
	}
}

func TestProgressLoopLargeFileCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.bin")
	write := func(c string) {
		if err := os.WriteFile(path, append(make([]byte, 2<<20), c...), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("A")
	call := msg.NewToolCall("write", "write", map[string]any{"path": "big.bin"})

	// Default cap (1 MiB): a 2 MiB file is unknown, so no notice.
	g := progressLoopGuard{limit: 2, cwd: dir}
	if g.before(call).valid {
		t.Fatal("2 MiB file snapshotted under the 1 MiB default")
	}
	if event := fileCycle(t, &g, call, write); event.Notice != "" {
		t.Fatalf("file above the cap triggered: %+v", event)
	}

	// Raised cap: the same cycle is detected.
	write("A")
	g = progressLoopGuard{limit: 2, cwd: dir, maxFileBytes: 4 << 20}
	if event := fileCycle(t, &g, call, write); event.Notice == "" {
		t.Fatalf("2 MiB cycle under a 4 MiB cap not detected: %+v", event)
	}

	// The cap itself is clamped to 16 MiB.
	g = progressLoopGuard{maxFileBytes: 1 << 40}
	if g.fileCap() != MaxLoopFileBytes {
		t.Fatalf("cap not clamped: %d", g.fileCap())
	}
}

func TestProgressLoopHookRewrittenWritePathIsTracked(t *testing.T) {
	run := func(contents ...string) (notices int) {
		var turns []func() *msg.AssistantMessage
		for i, c := range contents {
			call := msg.NewToolCall(fmt.Sprintf("w%d", i), "write", map[string]any{"path": "original.txt", "content": c})
			turns = append(turns, func() *msg.AssistantMessage { return assistantToolCalls(call) })
		}
		turns = append(turns, func() *msg.AssistantMessage { return assistantText("done") })
		h, lane := newConcurrentTestRig(t, turns)
		h.opts.ProgressLoopLimit = 2
		if err := os.WriteFile(filepath.Join(h.opts.Cwd, "rewritten.txt"), []byte("A"), 0600); err != nil {
			t.Fatal(err)
		}
		h.Hooks().OnBeforeTool(func(_ context.Context, call msg.ToolCall) (BeforeToolResult, error) {
			raw, _ := json.Marshal(map[string]any{"path": "rewritten.txt", "content": call.Arguments["content"]})
			return BeforeToolResult{RewrittenArgs: raw}, nil
		})
		if _, err := lane.Prompt(context.Background(), "go", nil); err != nil {
			t.Fatal(err)
		}
		entries, err := lane.FindEntries(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if user, ok := entry.Message.(msg.UserMessage); ok && entry.Type == session.EntryMessage && user.KilnHook == "progress-loop" {
				notices++
			}
		}
		return notices
	}
	if n := run("B", "A"); n != 1 {
		t.Fatalf("A->B->A through a rewritten path: notices=%d, want 1", n)
	}
	if n := run("B", "C"); n != 0 {
		t.Fatalf("a real change through a rewritten path: notices=%d, want 0", n)
	}
}
