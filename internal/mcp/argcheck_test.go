package mcp

import (
	"encoding/json"
	"testing"

	"github.com/andrepato/harness/internal/tool"
)

// An MCP tool forwards keys its schema does not declare (the server owns
// its schema), but still refuses a case variant of a key the gate reads.
func TestToHarnessToolArgsPolicy(t *testing.T) {
	ht := ToHarnessTool(nil, McpTool{
		QualifiedName: "mcp__fs__write_file",
		Server:        "fs",
		Name:          "write_file",
		InputSchema:   json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
	})
	if err := tool.CheckArgsFor(ht, []byte(`{"path":"a","mode":"0644"}`)); err != nil {
		t.Fatalf("undeclared key refused for an MCP tool: %v", err)
	}
	if err := tool.CheckArgsFor(ht, []byte(`{"PATH":"/etc/x"}`)); err == nil {
		t.Fatal("case-variant key accepted for an MCP tool")
	}
	if err := tool.CheckArgsFor(ht, []byte(`{"path":"a","COMMAND":"x"}`)); err == nil {
		t.Fatal("case variant of a gate-read key accepted for an MCP tool")
	}
}
