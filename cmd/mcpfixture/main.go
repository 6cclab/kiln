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
package main

import (
	"context"
	"flag"
	"log"
	"os"

	"github.com/andrepato/harness/internal/testkit/mcpfixture"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	failStartup := flag.Bool("fail-startup", false, "exit 1 immediately, before serving anything")
	hang := flag.Bool("hang", false, "start but never complete MCP initialize")
	flag.Parse()

	if *failStartup {
		os.Exit(1)
	}

	if *hang {
		// Block forever without touching stdio, so the client's initialize
		// request is never answered. Only a process kill ends this.
		select {}
	}

	server := mcpfixture.NewServer()
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Printf("mcpfixture: server failed: %v", err)
		os.Exit(1)
	}
}
