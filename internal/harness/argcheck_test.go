package harness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
)

// runOneWrite runs a script whose model makes one write call with argsYAML
// (OUTSIDE replaced by a path outside the rig's cwd), with before as the
// only before_tool handler. It returns the call's result, how many times
// the handler ran, the rig's cwd and the outside path.
func runOneWrite(t *testing.T, argsYAML string, before func(msg.ToolCall) BeforeToolResult) (res *msg.ToolResultMessage, hookCalls int, cwd, outside string) {
	t.Helper()
	outside = filepath.Join(t.TempDir(), "zshrc")
	script := "model: faux-1\nsteps:\n" +
		"  - tool_call: {name: write, args: " + strings.ReplaceAll(argsYAML, "OUTSIDE", outside) + ", id: w1}\n" +
		"  - on_tool_result: w1\n" +
		"    then:\n" +
		"      - text: \"done\"\n"
	rig := newTestRig(t, script, nil)
	rig.H.Hooks().OnBeforeTool(func(ctx context.Context, call msg.ToolCall) (BeforeToolResult, error) {
		hookCalls++
		return before(call), nil
	})
	rig.H.Events().On(EventToolEnd, func(ev Event) { res = ev.ToolResult })
	if _, err := rig.mustLane("main").Prompt(context.Background(), "go", nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if res == nil {
		t.Fatal("no tool_end event")
	}
	return res, hookCalls, rig.H.opts.Cwd, outside
}

func assertNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s exists (stat err %v): the write tool ran", path, err)
	}
}

// The model's own input is checked before any before_tool handler (the
// permission gate is one) sees it.
func TestBeginToolRefusesCaseVariantKeysBeforeHooks(t *testing.T) {
	res, hookCalls, cwd, outside := runOneWrite(t, `{PATH: "OUTSIDE", content: x}`, func(msg.ToolCall) BeforeToolResult { return BeforeToolResult{} })
	assertNotExist(t, outside)
	assertNotExist(t, filepath.Join(cwd, "PATH"))
	if hookCalls != 0 {
		t.Errorf("before_tool ran %d times; the refusal must come first", hookCalls)
	}
	if text := msg.TextOf(res.Content); !res.IsError || !strings.Contains(text, `"PATH", which differs only in case from the parameter "path"`) {
		t.Errorf("result = %q (isError %v), want the case-variant refusal", text, res.IsError)
	}
}

// A hook's rewrite is held to the same rule before the tool runs.
func TestBeginToolRefusesCaseVariantKeysInRewrittenInput(t *testing.T) {
	var outsidePath string
	res, _, cwd, outside := runOneWrite(t, `{path: ok.txt, content: "OUTSIDE"}`, func(call msg.ToolCall) BeforeToolResult {
		outsidePath, _ = call.Arguments["content"].(string)
		raw, _ := json.Marshal(map[string]any{"PATH": outsidePath, "content": "x"})
		return BeforeToolResult{RewrittenArgs: raw}
	})
	if outsidePath != outside {
		t.Fatalf("hook saw content %q, want %q", outsidePath, outside)
	}
	assertNotExist(t, outside)
	assertNotExist(t, filepath.Join(cwd, "ok.txt"))
	if text := msg.TextOf(res.Content); !res.IsError || !strings.Contains(text, "only in case") {
		t.Errorf("result = %q (isError %v), want the case-variant refusal", text, res.IsError)
	}
}

// A rewrite that is not an object used to be ignored, running the
// original input the gate had never judged.
func TestBeginToolRefusesUnparsableRewrite(t *testing.T) {
	res, _, cwd, _ := runOneWrite(t, `{path: ok.txt, content: x}`, func(msg.ToolCall) BeforeToolResult {
		return BeforeToolResult{RewrittenArgs: json.RawMessage(`["not", "an", "object"]`)}
	})
	assertNotExist(t, filepath.Join(cwd, "ok.txt"))
	if text := msg.TextOf(res.Content); !res.IsError || !strings.Contains(text, "must be a JSON object") {
		t.Errorf("result = %q (isError %v), want a refusal", text, res.IsError)
	}
}

// Valid input still runs.
func TestBeginToolRunsExactKeys(t *testing.T) {
	res, hookCalls, cwd, _ := runOneWrite(t, `{path: ok.txt, content: x}`, func(msg.ToolCall) BeforeToolResult { return BeforeToolResult{} })
	if res.IsError || hookCalls != 1 {
		t.Fatalf("result = %q (isError %v), hook calls %d; want a normal write", msg.TextOf(res.Content), res.IsError, hookCalls)
	}
	if _, err := os.Stat(filepath.Join(cwd, "ok.txt")); err != nil {
		t.Fatalf("ok.txt not written: %v", err)
	}
}
