package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
)

func execTool(t *testing.T, tl *tool.Tool, args any) tool.Result {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	result, err := tl.Execute(context.Background(), raw, func(tool.Result) {}, tool.Invocation{ToolName: tl.Name})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return result
}

func resultText(r tool.Result) string {
	return msg.TextOf(r.Content)
}

func TestBashToolSuccess(t *testing.T) {
	env := execenv.New(t.TempDir())
	bt := BashTool(env)
	result := execTool(t, bt, map[string]any{"command": "echo hi"})
	if result.IsError {
		t.Fatalf("unexpected error result: %+v", result)
	}
	if strings.TrimSpace(resultText(result)) != "hi" {
		t.Fatalf("text = %q", resultText(result))
	}
}

func TestBashToolNonZeroExit(t *testing.T) {
	env := execenv.New(t.TempDir())
	bt := BashTool(env)
	result := execTool(t, bt, map[string]any{"command": "exit 3"})
	if !result.IsError {
		t.Fatal("expected IsError result")
	}
	if !strings.Contains(resultText(result), "Command exited with code 3") {
		t.Fatalf("text = %q", resultText(result))
	}
}

func TestBashToolNoOutput(t *testing.T) {
	env := execenv.New(t.TempDir())
	bt := BashTool(env)
	result := execTool(t, bt, map[string]any{"command": "true"})
	if resultText(result) != "(no output)" {
		t.Fatalf("text = %q", resultText(result))
	}
}

func TestBashToolStreamsPartialUpdates(t *testing.T) {
	env := execenv.New(t.TempDir())
	bt := BashTool(env)
	raw, _ := json.Marshal(map[string]any{"command": `for i in $(seq 1 20); do echo "l$i"; done`})
	var partials []tool.Result
	result, err := bt.Execute(context.Background(), raw, func(r tool.Result) {
		partials = append(partials, r)
	}, tool.Invocation{ToolName: "bash"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(partials) == 0 {
		t.Fatal("expected streamed partial updates")
	}
	if !strings.Contains(resultText(result), "l20") {
		t.Fatalf("final result missing last line: %q", resultText(result))
	}
}

func TestBashToolTruncationFooter(t *testing.T) {
	env := execenv.New(t.TempDir())
	bt := BashTool(env)
	// This is bash.go's own default (execenv.DefaultMaxLines = 2000), so
	// generate more lines than that to force truncation deterministically.
	result := execTool(t, bt, map[string]any{"command": `for i in $(seq 1 2500); do echo "l$i"; done`})
	if result.IsError {
		t.Fatalf("unexpected error: %+v", result)
	}
	text := resultText(result)
	if !strings.Contains(text, "Showing lines") {
		t.Fatalf("expected truncation footer, got %q", text[max(0, len(text)-300):])
	}
	if !strings.Contains(text, "l2500") {
		t.Fatal("expected tail retention to keep the last line")
	}
	if result.Details == nil {
		t.Fatal("expected Details to be populated for a truncated result")
	}
}

func TestBashToolInvalidTimeout(t *testing.T) {
	env := execenv.New(t.TempDir())
	bt := BashTool(env)
	result := execTool(t, bt, map[string]any{"command": "echo hi", "timeout": -1})
	if !result.IsError {
		t.Fatal("expected IsError for invalid timeout")
	}
}
