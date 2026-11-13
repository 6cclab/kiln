package mcp_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	mcpgate "github.com/andrepato/harness/internal/mcp"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
)

// fixtureTool connects the fixture server and returns its named tool as a
// harness tool, with oversized results saved under dir.
func fixtureTool(t *testing.T, name, dir string) *tool.Tool {
	t.Helper()
	bin := buildFixture(t)
	hub := mcpgate.NewHub()
	hub.ConnectAll(context.Background(), fixtureConfigs(bin))
	t.Cleanup(func() { hub.Close(context.Background()) })
	hub.SetResultDir(dir)
	for _, tl := range hub.Tools() {
		if tl.Name == name {
			return mcpgate.ToHarnessTool(hub, tl)
		}
	}
	t.Fatalf("%s tool not found", name)
	return nil
}

func callTool(t *testing.T, tl *tool.Tool, args map[string]any) tool.Result {
	t.Helper()
	raw, _ := json.Marshal(args)
	res, err := tl.Execute(context.Background(), raw, nil, tool.Invocation{ToolCallID: "call_1"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return res
}

func bigText(lines int) string {
	var b strings.Builder
	for i := 1; i <= lines; i++ {
		b.WriteString(strconv.Itoa(i) + "\n")
	}
	return b.String()
}

var savedPath = regexp.MustCompile(`Full output saved to: (/\S+\.txt)`)

// TestToHarnessToolOversizedResultSavedToFile: like Claude Code, a
// successful MCP result over 50,000 characters is not put in the
// conversation; it is saved to a file in the session scratchpad and the
// model gets a message naming the file, with a short preview.
func TestToHarnessToolOversizedResultSavedToFile(t *testing.T) {
	dir := t.TempDir()
	res := callTool(t, fixtureTool(t, "big", dir), map[string]any{"lines": 20000})
	text := msg.TextOf(res.Content)
	if len(text) > 5000 {
		t.Fatalf("result still carries %d characters; want the saved-file message", len(text))
	}
	m := savedPath.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("no saved path in result:\n%s", text)
	}
	if filepath.Dir(m[1]) != mustReal(t, dir) && filepath.Dir(m[1]) != dir {
		t.Errorf("saved to %s, want a file in %s", m[1], dir)
	}
	got, err := os.ReadFile(m[1])
	if err != nil {
		t.Fatal(err)
	}
	if want := bigText(20000); string(got) != want {
		t.Errorf("saved file has %d bytes, want the full %d-byte result", len(got), len(want))
	}
	if !strings.Contains(text, "Preview") || !strings.Contains(text, "\n1\n2\n3\n") {
		t.Errorf("result has no preview of the output:\n%s", text)
	}
}

// TestToHarnessToolResultUnderLimitStaysInline: a result under both limits
// is unchanged.
func TestToHarnessToolResultUnderLimitStaysInline(t *testing.T) {
	res := callTool(t, fixtureTool(t, "big", t.TempDir()), map[string]any{"lines": 2000})
	if got, want := msg.TextOf(res.Content), bigText(2000); got != want {
		t.Errorf("result changed: %d bytes, want %d", len(got), len(want))
	}
}

// TestToHarnessToolMaxOutputTokensEnv: MAX_MCP_OUTPUT_TOKENS lowers the
// token limit, as in Claude Code.
func TestToHarnessToolMaxOutputTokensEnv(t *testing.T) {
	t.Setenv("MAX_MCP_OUTPUT_TOKENS", "1000")
	res := callTool(t, fixtureTool(t, "big", t.TempDir()), map[string]any{"lines": 2000})
	if !savedPath.MatchString(msg.TextOf(res.Content)) {
		t.Errorf("a ~2,200-token result under MAX_MCP_OUTPUT_TOKENS=1000 was not saved:\n%.300s", msg.TextOf(res.Content))
	}
}

// TestToHarnessToolLongErrorKeepsHeadAndTail: an error result over about
// 11,000 characters keeps its first and last 5,000.
func TestToHarnessToolLongErrorKeepsHeadAndTail(t *testing.T) {
	long := "HEAD" + strings.Repeat("x", 20000) + "TAIL"
	res := callTool(t, fixtureTool(t, "fail", t.TempDir()), map[string]any{"message": long})
	text := msg.TextOf(res.Content)
	if !res.IsError {
		t.Fatal("want an error result")
	}
	if !strings.HasPrefix(text, "HEAD") && !strings.Contains(text[:200], "HEAD") {
		t.Errorf("head missing: %.80q", text)
	}
	if !strings.HasSuffix(strings.TrimSpace(text), "TAIL") {
		t.Errorf("tail missing: %.80q", text[len(text)-80:])
	}
	if len(text) > 10200 {
		t.Errorf("error result is %d characters; want about 10,000", len(text))
	}
}

func mustReal(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
