//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/testkit/screen"
)

// mcpToolSearchScript has the model discover the fixture's echo tool
// through tool_search, then call it: the path that only works if tools
// registered by the background connect reach both the gate and the tool
// set.
const mcpToolSearchScript = `model: faux-1
steps:
  - tool_call: {name: tool_search, args: {query: "echo text back"}, id: ts1}
  - on_tool_result: ts1
    then:
      - tool_call: {name: mcp__fixture__echo, args: {text: "round trip"}, id: e1}
  - on_tool_result: e1
    then:
      - text: "Echoed."
        usage: {input: 20, output: 5}
`

// TestTUI_MCP_BackgroundConnect starts the TUI with one MCP server and a
// server that never answers. The prompt must be usable before either has
// connected, the footer must report progress, and once the connect
// finishes the model must be able to find and call the fixture tool.
func TestTUI_MCP_BackgroundConnect(t *testing.T) {
	proj, home, sessDir, addr, srv := tuiFixture(t, mcpToolSearchScript)

	fixture := filepath.Join(t.TempDir(), "mcpfixture")
	build := exec.Command("go", "build", "-o", fixture, "../../cmd/mcpfixture")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build mcpfixture: %v\n%s", err, out)
	}
	cfg := filepath.Join(t.TempDir(), "mcp.json")
	cfgJSON := fmt.Sprintf(`{"mcpServers":{"fixture":{"command":%q},"hang":{"command":"sleep","args":["300"]}}}`, fixture)
	if err := os.WriteFile(cfg, []byte(cfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	tuiExtraOpts = []screen.Option{screen.WithEnv("HARNESS_MCP_CONNECT_TIMEOUT", "2s")}
	t.Cleanup(func() { tuiExtraOpts = nil })

	s := startTUI(t, 100, 30, proj, home, sessDir, addr, "--strict-mcp-config", "--mcp-config", cfg, "--permission-mode", "bypassPermissions")
	waitReady(t, s)

	// The first frame arrives while the servers are still connecting. The
	// connect goroutine's progress reaches the hint row above the input box
	// as a footer note (internal/cli/tui.go's ConnectMCP callback ->
	// MsgFooterNote; app.go's hintRow renders m.footer.Note() on the left).
	if err := s.WaitFor(regexp.MustCompile(`mcp: (connecting 2 servers|1/2 servers)`), 3*time.Second); err != nil {
		t.Fatalf("footer never showed MCP progress: %v", err)
	}
	// The hanging server times out; only the fixture connects. The failure
	// summary commits to the transcript once the connect goroutine
	// finishes — mcpFailureNotice (internal/cli/tui.go) reports it as "N
	// MCP servers unavailable · run /mcp" (singular here, since exactly one
	// of the two servers failed), not the per-server "no response within
	// Ns" text describeConnectError produces for /mcp's own detail view.
	if err := s.WaitFor("1 MCP server unavailable · run /mcp", 20*time.Second); err != nil {
		t.Fatalf("MCP failure notice never appeared: %v", err)
	}

	s.Send("echo something for me")
	s.SendKey("enter")
	if err := s.WaitFor(turnSummaryPattern, 20*time.Second); err != nil {
		t.Fatalf("turn never finished: %v", err)
	}
	rows := s.Rows()
	joined := ""
	for _, r := range rows {
		joined += r + "\n"
	}
	// tool_search is a read-only tool now (internal/tui/replay.go's
	// groupKindFor / app.go's msgCommitToolCall), so it collapses into the
	// grouped "Read N files" row rather than showing its own
	// "Tool_search(...)" header — matching the same read-only auto-allow
	// grouping this task's item 4 describes for Read/Glob/Grep. The MCP
	// tool's own call renders in kiln's "tool" block anatomy: a
	// "mcp__fixture__echo" label rule, then "Mcp__fixture__echo" (no
	// parens — transcript.go's RenderToolCall).
	for _, want := range []string{"Read 1 file", "Mcp__fixture__echo", "round trip", "Echoed."} {
		if !regexp.MustCompile(regexp.QuoteMeta(want)).MatchString(joined) {
			t.Errorf("screen missing %q:\n%s", want, joined)
		}
	}

	// The model was actually offered the tool after tool_search admitted it.
	reqs := srv()
	if len(reqs) == 0 {
		t.Fatal("faux recorded no requests")
	}
	if !bytes.Contains(reqs[len(reqs)-1], []byte(`"name":"mcp__fixture__echo"`)) {
		t.Errorf("last request does not offer mcp__fixture__echo:\n%s", reqs[len(reqs)-1])
	}
}
