package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

func loopRead(path, content string) (msg.ToolCall, msg.ToolResultMessage) {
	return msg.NewToolCall(path, "read", map[string]any{"path": path}), msg.ToolResultMessage{Content: msg.Blocks{msg.Text(content)}}
}

func TestProgressLoopRepeatedSuccessfulReadAndFileChange(t *testing.T) {
	g := progressLoopGuard{limit: 3}
	for i, content := range []string{"A", "A", "B", "A", "A"} {
		call, result := loopRead("file.txt", content)
		if event := g.observe(call, result, fileState{}); event.Notice != "" || event.Err != nil {
			t.Fatalf("read %d stopped after changed content: %+v", i, event)
		}
	}
	call, result := loopRead("file.txt", "A")
	if event := g.observe(call, result, fileState{}); event.Notice == "" || event.Err != nil {
		t.Fatalf("unchanged third read: %+v", event)
	}
	for range 2 {
		g.observe(call, result, fileState{})
	}
	if event := g.observe(call, result, fileState{}); event.Err == nil || event.Notice != "" {
		t.Fatalf("continued loop: %+v", event)
	}
}

func TestProgressLoopRepeatedReadOnlySequence(t *testing.T) {
	g := progressLoopGuard{limit: 2}
	for i, path := range []string{"a", "b", "a", "b"} {
		call, result := loopRead(path, path+" contents")
		event := g.observe(call, result, fileState{})
		if (event.Notice != "") != (i == 3) || event.Err != nil {
			t.Fatalf("call %d: %+v", i, event)
		}
	}
}

func TestProgressLoopSuccessfulBashSequenceObservesWorkspace(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "state.txt")
	if err := os.WriteFile(path, []byte("A"), 0600); err != nil {
		t.Fatal(err)
	}
	g := progressLoopGuard{limit: 2, cwd: cwd}
	var event progressLoopEvent
	for i, command := range []string{"pwd", "ls", "pwd", "ls"} {
		call := msg.NewToolCall(command, "bash", map[string]any{"command": command})
		event = g.observe(call, msg.ToolResultMessage{Content: msg.Blocks{msg.Text(command)}}, fileState{})
		if i < 3 && (event.Notice != "" || event.Err != nil) {
			t.Fatalf("early notice after %d calls: %+v", i+1, event)
		}
	}
	if event.Notice == "" || event.Err != nil {
		t.Fatalf("unchanged successful Bash sequence: %+v", event)
	}
	g = progressLoopGuard{limit: 2, cwd: cwd}
	for i, command := range []string{"pwd", "ls", "pwd", "ls"} {
		if i == 2 {
			if err := os.WriteFile(path, []byte("B"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		call := msg.NewToolCall(command, "bash", map[string]any{"command": command})
		if event := g.observe(call, msg.ToolResultMessage{Content: msg.Blocks{msg.Text(command)}}, fileState{}); event.Notice != "" || event.Err != nil {
			t.Fatalf("changed workspace triggered loop after call %d: %+v", i+1, event)
		}
	}
}

func TestProgressLoopSuccessfulBashSequenceInTurn(t *testing.T) {
	call := func(id, command string) msg.ToolCall {
		return msg.NewToolCall(id, "bash", map[string]any{"command": command})
	}
	turns := []func() *msg.AssistantMessage{
		func() *msg.AssistantMessage {
			return assistantToolCalls(call("one", "pwd"), call("two", "ls"), call("three", "pwd"), call("four", "ls"))
		},
		func() *msg.AssistantMessage { return &msg.AssistantMessage{Content: msg.Blocks{msg.Text("done")}} },
	}
	h, lane := newConcurrentTestRig(t, turns)
	h.opts.ProgressLoopLimit = 2
	if _, err := lane.Prompt(context.Background(), "inspect", nil); err != nil {
		t.Fatal(err)
	}
	entries, err := lane.FindEntries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var notices int
	for _, entry := range entries {
		if user, ok := entry.Message.(msg.UserMessage); ok && user.KilnHook == "progress-loop" {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("Bash sequence corrective notices = %d, want one", notices)
	}
}

func TestProgressLoopBashUnknownStateDoesNotTrigger(t *testing.T) {
	cwd := t.TempDir()
	for i := range 257 {
		if err := os.WriteFile(filepath.Join(cwd, fmt.Sprintf("file-%03d", i)), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	g := progressLoopGuard{limit: 2, cwd: cwd}
	for _, command := range []string{"pwd", "ls", "pwd", "ls"} {
		call := msg.NewToolCall(command, "bash", map[string]any{"command": command})
		if event := g.observe(call, msg.ToolResultMessage{Content: msg.Blocks{msg.Text(command)}}, fileState{}); event.Notice != "" || event.Err != nil {
			t.Fatalf("unknown large workspace claimed unchanged: %+v", event)
		}
	}
	if err := os.Remove(filepath.Join(cwd, "file-000")); err != nil {
		t.Fatal(err)
	}
	g = progressLoopGuard{limit: 2, cwd: cwd}
	for range 4 {
		call := msg.NewToolCall("write", "bash", map[string]any{"command": "printf change > file"})
		if event := g.observe(call, msg.ToolResultMessage{Content: msg.Blocks{msg.Text("same")}}, fileState{}); event.Notice != "" || event.Err != nil {
			t.Fatalf("effectful Bash claimed unchanged: %+v", event)
		}
	}
}

func TestProgressLoopWideDirectoryStopsAtEnumerationBound(t *testing.T) {
	cwd := t.TempDir()
	for i := range 1024 {
		if err := os.WriteFile(filepath.Join(cwd, fmt.Sprintf("file-%04d", i)), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	entries, known := boundedDirEntries(cwd, 256)
	if known || len(entries) > 257 {
		t.Fatalf("wide directory read %d entries with known=%v", len(entries), known)
	}
	g := progressLoopGuard{limit: 2, cwd: cwd}
	if _, known := g.workspaceDigest(); known {
		t.Fatal("wide directory was treated as a complete workspace observation")
	}
}

func TestProgressLoopOnlyEscalatesTheNoticedPattern(t *testing.T) {
	g := progressLoopGuard{limit: 2}
	for _, path := range []string{"a", "a", "b", "b"} {
		call, result := loopRead(path, "unchanged")
		if event := g.observe(call, result, fileState{}); event.Err != nil {
			t.Fatalf("different repeated read escalated: %+v", event)
		}
	}
	for _, path := range []string{"a", "a"} {
		call, result := loopRead(path, "unchanged")
		event := g.observe(call, result, fileState{})
		if path == "a" && event.Err != nil {
			return
		}
	}
	t.Fatal("original noticed loop did not escalate")
}

func TestProgressLoopEditRevertRestoresFileState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("A"), 0600); err != nil {
		t.Fatal(err)
	}
	g := progressLoopGuard{limit: 2, cwd: dir}
	call := msg.NewToolCall("edit", "edit", map[string]any{"path": "file.txt"})
	result := msg.ToolResultMessage{Content: msg.Blocks{msg.Text("edited")}}
	before := g.before(call)
	if err := os.WriteFile(path, []byte("B"), 0600); err != nil {
		t.Fatal(err)
	}
	if event := g.observe(call, result, before); event.Notice != "" || event.Err != nil {
		t.Fatal(event)
	}
	before = g.before(call)
	if err := os.WriteFile(path, []byte("A"), 0600); err != nil {
		t.Fatal(err)
	}
	if event := g.observe(call, result, before); event.Notice == "" || event.Err != nil {
		t.Fatalf("revert: %+v", event)
	}
}

func TestProgressLoopDefaultOff(t *testing.T) {
	g := progressLoopGuard{}
	for range 20 {
		call, result := loopRead("a", "same")
		if event := g.observe(call, result, fileState{}); event.Notice != "" || event.Err != nil {
			t.Fatal(event)
		}
	}
}

func TestProgressLoopNoticeThenEscalationInTurn(t *testing.T) {
	read := func(id string) msg.ToolCall { return msg.NewToolCall(id, "read", map[string]any{"path": "file.txt"}) }
	turns := []func() *msg.AssistantMessage{
		func() *msg.AssistantMessage { return assistantToolCalls(read("one"), read("two")) },
		func() *msg.AssistantMessage { return assistantToolCalls(read("three"), read("four")) },
	}
	h, lane := newConcurrentTestRig(t, turns)
	if err := os.WriteFile(filepath.Join(h.opts.Cwd, "file.txt"), []byte("same"), 0600); err != nil {
		t.Fatal(err)
	}
	h.opts.ProgressLoopLimit = 2
	result, err := lane.Prompt(context.Background(), "read", nil)
	if result.Status != StatusFailed || err == nil || !strings.Contains(err.Error(), "progress loop continued") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	entries, err := lane.FindEntries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	notices := 0
	for _, entry := range entries {
		if entry.Type != session.EntryMessage {
			continue
		}
		if user, ok := entry.Message.(msg.UserMessage); ok && user.KilnHook == "progress-loop" {
			notices++
			if !strings.Contains(user.Content[0].(msg.TextContent).Text, "Stop repeating") {
				t.Fatal(user)
			}
		}
	}
	if notices != 1 {
		t.Fatalf("corrective notices = %d", notices)
	}
}
