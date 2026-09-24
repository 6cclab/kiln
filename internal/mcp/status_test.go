package mcp_test

import (
	"strings"
	"testing"

	mcpgate "github.com/andrepato/harness/internal/mcp"
)

func TestRenderMCPReportEmpty(t *testing.T) {
	got := mcpgate.RenderMCPReport(nil)
	want := "No MCP servers configured. They are read from ~/.claude.json"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRenderMCPReportSortsAndTruncates(t *testing.T) {
	statuses := []mcpgate.ServerStatus{
		{Name: "small", OK: true, ToolCount: 2, Ms: 10},
		{Name: "big", OK: true, ToolCount: 20, Ms: 5},
		{Name: "dead", OK: false, Error: strings.Repeat("x", 200), Ms: 1},
	}
	got := mcpgate.RenderMCPReport(statuses)

	if !strings.HasPrefix(got, "2/3 connected, 22 tools\n") {
		t.Fatalf("unexpected summary line: %q", got)
	}
	bigIdx := strings.Index(got, "big")
	smallIdx := strings.Index(got, "small")
	if bigIdx == -1 || smallIdx == -1 || bigIdx > smallIdx {
		t.Fatalf("expected big (more tools) before small: %q", got)
	}
	deadLine := ""
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "dead") {
			deadLine = line
		}
	}
	if !strings.Contains(deadLine, "failed:") {
		t.Fatalf("expected failed line for dead server: %q", deadLine)
	}
	if strings.Contains(deadLine, strings.Repeat("x", 200)) {
		t.Fatalf("expected error to be truncated: %q", deadLine)
	}
}

func TestRenderToolsReport(t *testing.T) {
	got := mcpgate.RenderToolsReport([]string{"bash", "read"})
	want := "2 resident:\n  bash\n  read"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
