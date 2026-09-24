// Package mcp is the MCP (Model Context Protocol) client hub: reading
// ~/.claude.json server configs, connecting to each configured server over
// stdio or Streamable HTTP, adapting their tools into harness's tool.Tool
// interface, and gating which of those tools are actually loaded into a
// session's active set.
//
// Ported from harness/src/mcp/client.ts and harness/src/mcp/gating.ts. See
// those files' comments for the measurements ("165 tools across 9 servers
// costs 97% of a 32k window") behind the design.
package mcp
