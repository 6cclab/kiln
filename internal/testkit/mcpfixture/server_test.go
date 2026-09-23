package mcpfixture_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/testkit/mcpfixture"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// buildFixture compiles the cmd/mcpfixture binary for use by the go-sdk
// client over a real stdio subprocess, exercising the exact transport
// harness will use against real MCP servers.
func buildFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "mcpfixture")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/andrepato/harness/cmd/mcpfixture")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("building mcpfixture binary: %v", err)
	}
	return bin
}

func connect(t *testing.T, bin string, extraArgs ...string) *mcp.ClientSession {
	t.Helper()
	transport := &mcp.CommandTransport{Command: exec.Command(bin, extraArgs...)}
	client := mcp.NewClient(&mcp.Implementation{Name: "mcpfixture-test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestFixtureListTools(t *testing.T) {
	bin := buildFixture(t)
	cs := connect(t, bin)

	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if got := len(res.Tools); got != 4 {
		names := make([]string, len(res.Tools))
		for i, tl := range res.Tools {
			names[i] = tl.Name
		}
		t.Fatalf("expected 4 tools, got %d: %v", got, names)
	}

	want := map[string]bool{"echo": false, "slow": false, "fail": false, "big": false}
	for _, tl := range res.Tools {
		if _, ok := want[tl.Name]; !ok {
			t.Fatalf("unexpected tool %q", tl.Name)
		}
		want[tl.Name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("expected tool %q, not seen", name)
		}
	}
}

func TestFixtureEcho(t *testing.T) {
	bin := buildFixture(t)
	cs := connect(t, bin)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "echo",
		Arguments: map[string]any{"text": "hello fixture"},
	})
	if err != nil {
		t.Fatalf("CallTool echo: %v", err)
	}
	if res.IsError {
		t.Fatalf("echo reported IsError: %+v", res.Content)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}
	if text.Text != "hello fixture" {
		t.Fatalf("echo: got %q, want %q", text.Text, "hello fixture")
	}
}

func TestFixtureFail(t *testing.T) {
	bin := buildFixture(t)
	cs := connect(t, bin)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "fail",
		Arguments: map[string]any{"message": "boom"},
	})
	if err != nil {
		t.Fatalf("CallTool fail: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected fail tool to set IsError, got %+v", res)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok || !strings.Contains(text.Text, "boom") {
		t.Fatalf("expected error content to mention %q, got %+v", "boom", res.Content)
	}
}

func TestFixtureSlow(t *testing.T) {
	bin := buildFixture(t)
	cs := connect(t, bin)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "slow",
		Arguments: map[string]any{"ms": 10},
	})
	if err != nil {
		t.Fatalf("CallTool slow: %v", err)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok || text.Text != "slept 10ms" {
		t.Fatalf("slow: got %+v, want %q", res.Content, "slept 10ms")
	}
}

func TestFixtureBig(t *testing.T) {
	bin := buildFixture(t)
	cs := connect(t, bin)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "big",
		Arguments: map[string]any{"lines": 5},
	})
	if err != nil {
		t.Fatalf("CallTool big: %v", err)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}
	lines := strings.Split(strings.TrimRight(text.Text, "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d: %q", len(lines), text.Text)
	}
	if lines[0] != "1" || lines[4] != "5" {
		t.Fatalf("unexpected line contents: %q", lines)
	}
}

func TestFixtureFailStartup(t *testing.T) {
	bin := buildFixture(t)
	cmd := exec.Command(bin, "--fail-startup")
	err := cmd.Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected ExitError from --fail-startup, got %v (%T)", err, err)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit code 1, got %d", exitErr.ExitCode())
	}
}

func TestClaudeJSON(t *testing.T) {
	got := mcpfixture.ClaudeJSON("/path/to/fixture-binary")

	var parsed struct {
		MCPServers map[string]struct {
			Command string `json:"command"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("ClaudeJSON did not produce valid JSON: %v\n%s", err, got)
	}
	entry, ok := parsed.MCPServers["fixture"]
	if !ok {
		t.Fatalf("ClaudeJSON missing mcpServers.fixture entry: %s", got)
	}
	if entry.Command != "/path/to/fixture-binary" {
		t.Fatalf("ClaudeJSON command = %q, want %q", entry.Command, "/path/to/fixture-binary")
	}
}
