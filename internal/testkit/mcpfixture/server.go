// Package mcpfixture provides a small, deterministic MCP server used by
// harness's Go test suite to exercise MCP client behaviour (tool listing,
// tool calls, error results, slow calls, truncation, dead servers and
// servers that hang during initialize) without depending on a real MCP
// server implementation.
//
// The server is exposed both as a library (NewServer) for in-process use
// (e.g. over mcp.NewInMemoryTransports) and as the cmd/mcpfixture binary for
// out-of-process stdio use, which is what most harness tests want since it
// exercises the same code path the real product uses to launch MCP servers.
package mcpfixture

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Name is the MCP server name reported by NewServer.
const Name = "fixture"

// Version is the MCP server version reported by NewServer.
const Version = "0.0.1"

// NewServer builds the "fixture" MCP server with its four test tools:
//
//   - echo{text}: returns text unchanged.
//   - slow{ms}: sleeps for ms milliseconds, then returns "slept <ms>ms".
//   - fail{message}: always returns an error result (isError true).
//   - big{lines}: returns N numbered lines, for truncation tests.
func NewServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: Name, Version: Version}, nil)

	type echoArgs struct {
		Text string `json:"text" jsonschema:"text to echo back"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "echo",
		Description: "echo back the given text",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args echoArgs) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: args.Text}},
		}, nil, nil
	})

	type slowArgs struct {
		MS int `json:"ms" jsonschema:"milliseconds to sleep before returning"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "slow",
		Description: "sleep for the given number of milliseconds, then return",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args slowArgs) (*mcp.CallToolResult, any, error) {
		timer := time.NewTimer(time.Duration(args.MS) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("slept %dms", args.MS)}},
		}, nil, nil
	})

	type failArgs struct {
		Message string `json:"message" jsonschema:"error message to return"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "fail",
		Description: "always fail with the given message",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args failArgs) (*mcp.CallToolResult, any, error) {
		// Returning an error from a ToolHandlerFor causes the SDK to build a
		// CallToolResult with IsError set to true and the error text as
		// content, rather than an MCP protocol-level error response. That is
		// exactly the "fail" tool's contract.
		return nil, nil, fmt.Errorf("%s", args.Message)
	})

	type bigArgs struct {
		Lines int `json:"lines" jsonschema:"number of numbered lines to return"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "big",
		Description: "return N numbered lines, for truncation tests",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args bigArgs) (*mcp.CallToolResult, any, error) {
		var b strings.Builder
		for i := 1; i <= args.Lines; i++ {
			fmt.Fprintf(&b, "%d\n", i)
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: b.String()}},
		}, nil, nil
	})

	return server
}

// claudeMCPServer mirrors the shape Claude Code's ~/.claude.json uses for a
// single stdio MCP server entry.
type claudeMCPServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

// claudeConfig mirrors the top-level ~/.claude.json shape harness cares
// about: a map of MCP server name to its stdio launch command.
type claudeConfig struct {
	MCPServers map[string]claudeMCPServer `json:"mcpServers"`
}

// ClaudeJSON returns a ~/.claude.json-shaped JSON snippet wiring the fixture
// binary at binaryPath as an MCP server named "fixture":
//
//	{"mcpServers":{"fixture":{"command":"<binaryPath>"}}}
//
// It is meant to be embedded (or merged) into a test's ~/.claude.json
// fixture so that harness discovers and launches the fixture server the
// same way it would launch a real one.
func ClaudeJSON(binaryPath string) string {
	cfg := claudeConfig{
		MCPServers: map[string]claudeMCPServer{
			Name: {Command: binaryPath},
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		// cfg is a fixed, JSON-safe shape; Marshal cannot fail on it.
		panic(err)
	}
	return string(b)
}
