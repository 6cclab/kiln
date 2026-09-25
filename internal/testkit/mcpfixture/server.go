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
//
// A Server returned by NewServer also keeps an in-memory log of every tool
// call it receives, inspectable via Requests and clearable via Reset. This
// only observes in-process callers; the stdio binary runs in a separate
// process, so its calls are not logged here.
package mcpfixture

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Name is the MCP server name reported by NewServer.
const Name = "fixture"

// Version is the MCP server version reported by NewServer.
const Version = "0.0.1"

// ToolCall records one tool invocation received by a Server, in the order
// it arrived. Args is the raw JSON the client sent (not re-marshaled from
// the decoded struct), so a test can assert on the exact bytes a real MCP
// client would have produced.
type ToolCall struct {
	Tool string
	Args json.RawMessage
	Seq  int
	Time time.Time
}

// Server wraps the go-sdk *mcp.Server with an in-memory log of every tool
// call it receives. The log only observes in-process use (NewServer callers
// holding onto the *Server, e.g. over mcp.NewInMemoryTransports); the
// cmd/mcpfixture stdio binary runs in a separate process and so cannot be
// inspected this way, but embeds and runs the same *mcp.Server underneath.
type Server struct {
	*mcp.Server

	mu    sync.Mutex
	calls []ToolCall
}

// log appends a ToolCall for tool with raw argument bytes args, assigning
// it the next sequence number.
func (s *Server) log(tool string, args json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, ToolCall{
		Tool: tool,
		Args: append(json.RawMessage(nil), args...),
		Seq:  len(s.calls),
		Time: time.Now(),
	})
}

// Requests returns every tool call recorded so far, in order.
func (s *Server) Requests() []ToolCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ToolCall, len(s.calls))
	copy(out, s.calls)
	return out
}

// Reset clears the recorded tool call log.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = nil
}

// NewServer builds the "fixture" MCP server with its four test tools:
//
//   - echo{text}: returns text unchanged.
//   - slow{ms}: sleeps for ms milliseconds, then returns "slept <ms>ms".
//   - fail{message}: always returns an error result (isError true).
//   - big{lines}: returns N numbered lines, for truncation tests.
func NewServer() *Server {
	s := &Server{Server: mcp.NewServer(&mcp.Implementation{Name: Name, Version: Version}, nil)}
	server := s.Server

	type echoArgs struct {
		Text string `json:"text" jsonschema:"text to echo back"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "echo",
		Description: "echo back the given text",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args echoArgs) (*mcp.CallToolResult, any, error) {
		s.log("echo", req.Params.Arguments)
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
		s.log("slow", req.Params.Arguments)
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
		s.log("fail", req.Params.Arguments)
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
		s.log("big", req.Params.Arguments)
		var b strings.Builder
		for i := 1; i <= args.Lines; i++ {
			fmt.Fprintf(&b, "%d\n", i)
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: b.String()}},
		}, nil, nil
	})

	return s
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
