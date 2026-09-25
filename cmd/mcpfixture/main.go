// Command mcpfixture runs the mcpfixture test MCP server over stdio.
//
// It exists so harness's Go test suite can launch a real MCP server
// subprocess (rather than an in-process transport) and exercise the exact
// code path harness uses to talk to a configured MCP server.
//
// Flags:
//
//	--fail-startup   exit 1 immediately, before serving anything. Simulates
//	                 a dead/misconfigured MCP server for dead-server
//	                 rendering tests.
//	--hang           accept the process start but never respond to
//	                 anything on stdio, so the client's initialize call
//	                 never completes. Simulates a server that hangs during
//	                 the MCP handshake, for connect-timeout tests.
//	--connect-delay  sleep for the given duration (e.g. "3s") before
//	                 reading stdin at all, so the client's initialize
//	                 request sits unanswered until the delay elapses. Unlike
//	                 --hang this is time-bounded: the server serves normally
//	                 once the delay passes. Exists solely for
//	                 test/e2e/mcp_behaviour_test.go's
//	                 TestMCP_FirstTurnDoesNotWaitForConnect, which needs a
//	                 connect that is slow but eventually succeeds, to prove
//	                 the harness's first turn does not block on it.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/andrepato/harness/internal/testkit/mcpfixture"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	os.Exit(Main(os.Args[1:], os.Stdout, os.Stderr))
}

// Main parses flags and runs the mcpfixture server. It returns a process
// exit code rather than calling os.Exit so tests can drive it directly.
//
// Two flag combinations never return: --hang blocks forever by design (only
// a process kill ends it), and the default path (no --fail-startup) serves
// the MCP protocol over stdio until the client disconnects or the process is
// killed. Callers that need a bounded call use --fail-startup or an invalid
// flag, both of which return promptly.
func Main(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcpfixture", flag.ContinueOnError)
	fs.SetOutput(stderr)
	failStartup := fs.Bool("fail-startup", false, "exit 1 immediately, before serving anything")
	hang := fs.Bool("hang", false, "start but never complete MCP initialize")
	connectDelay := fs.Duration("connect-delay", 0, "sleep this long before completing MCP initialize (test only)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *failStartup {
		return 1
	}

	if *hang {
		// Block forever without touching stdio, so the client's initialize
		// request is never answered. Only a process kill ends this.
		select {}
	}

	if *connectDelay > 0 {
		// server.Run below is what starts reading stdin at all, so
		// sleeping before calling it delays the client's initialize
		// response by exactly this long without touching protocol
		// internals.
		time.Sleep(*connectDelay)
	}

	server := mcpfixture.NewServer()
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintf(stderr, "mcpfixture: server failed: %v\n", err)
		return 1
	}
	return 0
}
