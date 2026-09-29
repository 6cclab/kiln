// Command incident-mcp is a small, deterministic stdio MCP server used by
// kiln's real-model QA scenarios to exercise an on-call/incident-response
// workflow: a user points kiln at this MCP server and asks it to
// investigate a paging alert on "orders-svc", then fix the underlying bug
// in the qa/fixtures/real/mcp-orders-svc fixture and add a regression test.
//
// It follows the same shape as cmd/mcpfixture/main.go and
// internal/testkit/mcpfixture/server.go: a real go-sdk MCP server run over
// stdio, but with canned, deterministic data describing one fictional
// incident (orders-svc paginating past the end of its order list and
// panicking) instead of generic test tools.
//
// Build: go build -o <out> ./qa/fixtures/incident-mcp
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	os.Exit(run())
}

func run() int {
	server := mcp.NewServer(&mcp.Implementation{Name: "incidents", Version: "0.0.1"}, nil)
	registerTools(server)
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintf(os.Stderr, "incident-mcp: server failed: %v\n", err)
		return 1
	}
	return 0
}
