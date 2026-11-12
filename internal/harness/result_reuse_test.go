package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// runReads runs one read per turn of file.txt (identical arguments) and calls
// mutate(i) before turn i's call executes. It returns each result's text.
func runReads(t *testing.T, reuse bool, n int, mutate func(cwd string, i int)) []string {
	t.Helper()
	var cwd string
	var turns []func() *msg.AssistantMessage
	for i := 0; i < n; i++ {
		call := msg.NewToolCall(fmt.Sprintf("call%d", i), "read", map[string]any{"path": "file.txt"})
		turns = append(turns, func() *msg.AssistantMessage {
			if mutate != nil {
				mutate(cwd, i)
			}
			return assistantToolCalls(call)
		})
	}
	turns = append(turns, func() *msg.AssistantMessage { return assistantText("done") })
	h, lane := newConcurrentTestRig(t, turns)
	cwd = h.opts.Cwd
	h.opts.ReuseUnchangedResults = reuse
	if err := os.WriteFile(filepath.Join(cwd, "file.txt"), []byte(strings.Repeat("line of content\n", 40)), 0600); err != nil {
		t.Fatal(err)
	}
	if res, err := lane.Prompt(context.Background(), "go", nil); err != nil || res.Status != StatusCompleted {
		t.Fatalf("%+v %v", res, err)
	}
	entries, err := lane.FindEntries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, entry := range entries {
		if r, ok := entry.Message.(msg.ToolResultMessage); ok && entry.Type == session.EntryMessage {
			out = append(out, r.Content[0].(msg.TextContent).Text)
		}
	}
	slices.Reverse(out) // FindEntries is newest first
	return out
}

func TestUnchangedReadResultIsReusedWhenOptedIn(t *testing.T) {
	got := runReads(t, true, 3, nil)
	if len(got) != 3 || !strings.Contains(got[0], "line of content") {
		t.Fatalf("first read must be full: %q", got)
	}
	for i := 1; i < 3; i++ {
		if !strings.HasPrefix(got[i], "[Unchanged since call call0:") || len(got[i]) > 200 {
			t.Fatalf("repeat %d not reused: %q", i, got[i])
		}
	}
}

func TestUnchangedReadResultNotReusedWithoutOptIn(t *testing.T) {
	for i, text := range runReads(t, false, 2, nil) {
		if !strings.Contains(text, "line of content") {
			t.Fatalf("result %d replaced while reuse is off: %q", i, text)
		}
	}
}

func TestChangedReadResultIsNeverReused(t *testing.T) {
	got := runReads(t, true, 3, func(cwd string, i int) {
		if i == 1 { // the file changes before the second read
			_ = os.WriteFile(filepath.Join(cwd, "file.txt"), []byte(strings.Repeat("different text!\n", 40)), 0600)
		}
	})
	if !strings.Contains(got[1], "different text!") || strings.Contains(got[1], "Unchanged") {
		t.Fatalf("changed file reused: %q", got[1])
	}
	// The third read matches the second, which is the new reference.
	if !strings.HasPrefix(got[2], "[Unchanged since call call1:") {
		t.Fatalf("repeat of the new content: %q", got[2])
	}
}

func TestReuseRequiresEarlierResultStillInTranscript(t *testing.T) {
	h, lane := newConcurrentTestRig(t, []func() *msg.AssistantMessage{func() *msg.AssistantMessage { return assistantText("x") }})
	h.opts.ReuseUnchangedResults = true
	call := func(id string) msg.ToolCall { return msg.NewToolCall(id, "read", map[string]any{"path": "f"}) }
	full := func() msg.ToolResultMessage {
		return msg.ToolResultMessage{Content: msg.Blocks{msg.Text(strings.Repeat("y", 400))}}
	}
	args := map[string]any{"path": "f"}

	first := full()
	lane.noteLive(nil)
	lane.reuseUnchanged(call("a"), args, &first)

	// "a" was compacted away: the repeat must stay full and become the reference.
	lane.noteLive([]msg.Message{msg.ToolResultMessage{ToolCallID: "unrelated"}})
	second := full()
	lane.reuseUnchanged(call("b"), args, &second)
	if len(second.Content[0].(msg.TextContent).Text) != 400 {
		t.Fatalf("pointer to a compacted result: %v", second.Content)
	}
	// "b" is visible: the next identical result points at it.
	lane.noteLive([]msg.Message{msg.ToolResultMessage{ToolCallID: "b"}})
	third := full()
	lane.reuseUnchanged(call("c"), args, &third)
	if text := third.Content[0].(msg.TextContent).Text; !strings.HasPrefix(text, "[Unchanged since call b:") {
		t.Fatalf("visible earlier result not referenced: %q", text)
	}
	// Errors and short results are never replaced.
	short := msg.ToolResultMessage{Content: msg.Blocks{msg.Text("tiny")}}
	lane.reuseUnchanged(call("d"), args, &short)
	lane.reuseUnchanged(call("e"), args, &short)
	if short.Content[0].(msg.TextContent).Text != "tiny" {
		t.Fatal("short result replaced")
	}
}
