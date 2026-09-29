package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/andrepato/harness/internal/testkit/mcpfixture"
)

// realEnviron is the process environment captured at package init, before
// any test's t.Setenv("HOME", ...) mutates it. buildMCPFixture needs it: a
// `go build` invoked with a scratch $HOME (as every MCP test sets, via
// scratchProject) would otherwise treat that scratch directory as its
// GOPATH/module cache and try to re-download the entire module graph into
// it, which is both slow and (since t.TempDir() cleans up mid-run) racy.
var realEnviron = os.Environ()

// repoRoot is this file's directory's grandparent (internal/cli -> repo
// root), resolved via runtime.Caller so it does not depend on the test's
// current working directory (scratchProject chdirs into a scratch project
// before most tests call buildMCPFixture).
func repoRoot() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "..", "..")
}

var (
	fixtureBuildOnce sync.Once
	fixtureBinPath   string
	fixtureBuildErr  error
)

// buildMCPFixture compiles cmd/mcpfixture once for the whole test binary
// run (building it is slow enough, and it is used by several tests here,
// that doing it per-test would needlessly slow the suite down), against
// the real environment and repo root rather than whatever the calling
// test's scratch $HOME/cwd currently are.
func buildMCPFixture(t *testing.T) string {
	t.Helper()
	fixtureBuildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "harness-mcpfixture")
		if err != nil {
			fixtureBuildErr = err
			return
		}
		bin := filepath.Join(dir, "mcpfixture")
		cmd := exec.Command("go", "build", "-o", bin, "github.com/andrepato/harness/cmd/mcpfixture")
		cmd.Dir = repoRoot()
		cmd.Env = realEnviron
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fixtureBuildErr = err
			return
		}
		fixtureBinPath = bin
	})
	if fixtureBuildErr != nil {
		t.Fatalf("building mcpfixture binary: %v", fixtureBuildErr)
	}
	return fixtureBinPath
}

// writeClaudeJSON writes home/.claude.json wiring the fixture server named
// "fixture" at bin, with any extra args (e.g. "--fail-startup").
func writeClaudeJSON(t *testing.T, home, bin string, extraArgs ...string) {
	t.Helper()
	body := mcpfixture.ClaudeJSON(bin)
	if len(extraArgs) > 0 {
		// mcpfixture.ClaudeJSON has no args parameter; splice them in by
		// round-tripping through JSON so --fail-startup/--hang tests can
		// reuse it too.
		var parsed map[string]map[string]map[string]any
		if err := json.Unmarshal([]byte(body), &parsed); err != nil {
			t.Fatalf("parse mcpfixture.ClaudeJSON: %v", err)
		}
		entry := parsed["mcpServers"][mcpfixture.Name]
		entry["args"] = extraArgs
		parsed["mcpServers"][mcpfixture.Name] = entry
		raw, err := json.Marshal(parsed)
		if err != nil {
			t.Fatal(err)
		}
		body = string(raw)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRun_PrintMCP_Connects wires the fixture MCP server into a scratch
// ~/.claude.json and checks `harness -p "/mcp"` reports it connected with
// its 4 tools.
func TestRun_PrintMCP_Connects(t *testing.T) {
	startFaux(t, unreadScript)
	proj := scratchProject(t)
	_ = proj
	bin := buildMCPFixture(t)
	writeClaudeJSON(t, os.Getenv("HOME"), bin)

	args := baseArgs()
	args.Print = true
	args.PrintPrompt = "/mcp"

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit code %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "1/1 connected, 4 tools") {
		t.Errorf("stdout = %q, want it to contain \"1/1 connected, 4 tools\"", stdout.String())
	}
}

// TestRun_PrintTools_ListsResident checks `harness -p "/tools"` lists the
// resident tools, including tool_search once an MCP server is configured.
func TestRun_PrintTools_ListsResident(t *testing.T) {
	startFaux(t, unreadScript)
	scratchProject(t)
	bin := buildMCPFixture(t)
	writeClaudeJSON(t, os.Getenv("HOME"), bin)

	args := baseArgs()
	args.Print = true
	args.PrintPrompt = "/tools"

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit code %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"bash", "read", "edit", "write", "todo write", "tool search"} {
		if !strings.Contains(out, want) {
			t.Errorf("/tools output missing %q:\n%s", want, out)
		}
	}
}

// TestRun_ToolSearch_AdmitsMCPTool drives a faux script that calls
// tool_search, then the MCP tool tool_search admitted
// (mcp__fixture__echo), and checks: both tool calls succeed, and the
// request the harness sent AFTER tool_search's result (i.e. the one asking
// the model what to do next) already lists mcp__fixture__echo among its
// tools — proof that onAdmit re-gated the lane before that next turn was
// sent, not just that the call itself happened to work.
func TestRun_ToolSearch_AdmitsMCPTool(t *testing.T) {
	script := `model: faux-1
steps:
  - tool_call: {name: tool_search, args: {query: "echo"}, id: tc1}
  - on_tool_result: tc1
    then:
      - tool_call: {name: mcp__fixture__echo, args: {text: "hi"}, id: tc2}
  - on_tool_result: tc2
    then:
      - text: "done"
`
	srv := startFaux(t, script)
	scratchProject(t)
	// "fixture" is not in the "coding" posture's server list, so it must be
	// searchable for this test to exercise anything real.
	t.Setenv("HARNESS_POSTURE", "all")

	bin := buildMCPFixture(t)
	writeClaudeJSON(t, os.Getenv("HOME"), bin)

	args := baseArgs()
	args.Print = true
	args.PrintPrompt = "look up echo and use it"
	args.OutputFormat = "stream-json"
	args.PermissionMode = "bypassPermissions"

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit code %d, stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}

	var toolEnds []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		var ev map[string]any
		_ = json.Unmarshal([]byte(line), &ev)
		if ev["type"] == "tool_end" {
			toolEnds = append(toolEnds, ev)
		}
	}
	if len(toolEnds) != 2 {
		t.Fatalf("got %d tool_end events, want 2: %v", len(toolEnds), toolEnds)
	}
	for _, ev := range toolEnds {
		if isErr, _ := ev["isError"].(bool); isErr {
			t.Errorf("tool_end reported an error: %v", ev)
		}
	}

	reqs := srv.Requests()
	if len(reqs) < 2 {
		t.Fatalf("faux recorded %d requests, want at least 2", len(reqs))
	}
	sawEchoAfterAdmit := false
	for _, tl := range reqs[1].Tools {
		if tl.Name == "mcp__fixture__echo" {
			sawEchoAfterAdmit = true
		}
	}
	if !sawEchoAfterAdmit {
		var names []string
		for _, tl := range reqs[1].Tools {
			names = append(names, tl.Name)
		}
		t.Errorf("2nd request's tools = %v, want mcp__fixture__echo (onAdmit should have re-gated the lane)", names)
	}
}

// TestRun_PrintHelp_31Builtins checks /help lists all 31 built-in slash
// commands the registry wiring in commands.go registers — 29, /plugin
// (internal/commands/plugin_commands.go) and /plan (manage_commands.go).
func TestRun_PrintHelp_31Builtins(t *testing.T) {
	startFaux(t, unreadScript)
	scratchProject(t)

	args := baseArgs()
	args.Print = true
	args.PrintPrompt = "/help"

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit code %d, stderr=%s", code, stderr.String())
	}
	count := 0
	for _, line := range strings.Split(stdout.String(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "/") {
			count++
		}
	}
	if count != 31 {
		t.Errorf("/help listed %d commands, want 31:\n%s", count, stdout.String())
	}
}

// TestRun_PrintModel_SwitchesAndPrints checks `/model faux/faux-1` prints
// the switch confirmation line, matching builtins.ts's own wording.
func TestRun_PrintModel_SwitchesAndPrints(t *testing.T) {
	startFaux(t, unreadScript)
	scratchProject(t)

	args := baseArgs()
	args.Print = true
	args.PrintPrompt = "/model faux/faux-1"

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit code %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Now on faux/faux-1") {
		t.Errorf("stdout = %q, want it to contain \"Now on faux/faux-1\"", stdout.String())
	}
}

// TestRun_StrictMCPConfig_NoFile_ConnectsNothing checks --strict-mcp-config
// with no --mcp-config file connects to nothing, even when a default
// ~/.claude.json exists with servers configured.
func TestRun_StrictMCPConfig_NoFile_ConnectsNothing(t *testing.T) {
	startFaux(t, unreadScript)
	scratchProject(t)
	bin := buildMCPFixture(t)
	writeClaudeJSON(t, os.Getenv("HOME"), bin)

	args := baseArgs()
	args.Print = true
	args.PrintPrompt = "/mcp"
	args.StrictMCPConfig = true

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit code %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "0/0") {
		t.Errorf("stdout = %q, want it to report 0/0 connected", stdout.String())
	}
}

// TestRun_PrintMCP_DeadServer checks a server that exits immediately
// (--fail-startup) shows up in /mcp as failed, and the run still completes
// (one broken server must not deny the whole session).
func TestRun_PrintMCP_DeadServer(t *testing.T) {
	startFaux(t, unreadScript)
	scratchProject(t)
	bin := buildMCPFixture(t)
	writeClaudeJSON(t, os.Getenv("HOME"), bin, "--fail-startup")

	args := baseArgs()
	args.Print = true
	args.PrintPrompt = "/mcp"

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit code %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "0/1 connected") {
		t.Errorf("stdout = %q, want it to report 0/1 connected", stdout.String())
	}
	if !strings.Contains(stdout.String(), "failed:") {
		t.Errorf("stdout = %q, want a failed server line", stdout.String())
	}
}

// TestMCP_Subcommand_Connects checks `harness mcp` (the subcommand, not the
// slash command) connects for real and reports the fixture server.
func TestMCP_Subcommand_Connects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := buildMCPFixture(t)
	writeClaudeJSON(t, home, bin)

	var stdout, stderr bytes.Buffer
	code := MCP(context.Background(), baseArgs(), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "1/1 connected, 4 tools") {
		t.Errorf("stdout = %q, want it to contain \"1/1 connected, 4 tools\"", stdout.String())
	}
}

// TestDoctor_ReportsMCP checks `harness doctor` reports real MCP
// connection status and the resident tool count/strategy, rather than the
// old "not connected yet (phase 5)" placeholder.
func TestDoctor_ReportsMCP(t *testing.T) {
	startFaux(t, unreadScript)
	scratchProject(t)
	bin := buildMCPFixture(t)
	writeClaudeJSON(t, os.Getenv("HOME"), bin)

	args := baseArgs()
	var stdout, stderr bytes.Buffer
	code := Doctor(context.Background(), args, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "mcp        1/1 connected") {
		t.Errorf("doctor output = %q, want \"mcp        1/1 connected\"", out)
	}
	if strings.Contains(out, "phase 5") {
		t.Errorf("doctor output still mentions phase 5: %q", out)
	}
	if !strings.Contains(out, "always available · MCP index") || strings.Contains(out, "resident") {
		t.Errorf("doctor output missing resident tool/strategy line: %q", out)
	}
}
