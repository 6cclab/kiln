package mcp_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpgate "github.com/andrepato/harness/internal/mcp"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/testkit/mcpfixture"
	"github.com/andrepato/harness/internal/tool"
)

// buildFixture compiles cmd/mcpfixture once per test binary run, mirroring
// internal/testkit/mcpfixture/server_test.go's own buildFixture helper.
func buildFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "mcpfixture")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/andrepato/harness/cmd/mcpfixture")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("building mcpfixture binary: %v", err)
	}
	return bin
}

func fixtureConfigs(bin string, extraArgs ...string) map[string]mcpgate.ServerConfig {
	return map[string]mcpgate.ServerConfig{
		"fixture": {Command: bin, Args: extraArgs},
	}
}

func TestConnectAllListsFixtureTools(t *testing.T) {
	bin := buildFixture(t)
	hub := mcpgate.NewHub()
	hub.ConnectAll(context.Background(), fixtureConfigs(bin))
	defer hub.Close(context.Background())

	statuses := hub.Statuses()
	if len(statuses) != 1 || !statuses[0].OK {
		t.Fatalf("expected fixture to connect ok, got %+v", statuses)
	}
	if statuses[0].ToolCount != 4 {
		t.Fatalf("expected 4 tools, got %d", statuses[0].ToolCount)
	}

	tools := hub.Tools()
	want := map[string]bool{
		"mcp__fixture__echo": false, "mcp__fixture__slow": false,
		"mcp__fixture__fail": false, "mcp__fixture__big": false,
	}
	for _, tl := range tools {
		if _, ok := want[tl.QualifiedName]; !ok {
			t.Fatalf("unexpected tool %q", tl.QualifiedName)
		}
		want[tl.QualifiedName] = true
		if tl.Server != "fixture" {
			t.Errorf("tool %q server = %q, want fixture", tl.QualifiedName, tl.Server)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("expected tool %q, not seen", name)
		}
	}
}

func TestToHarnessToolEchoRoundTrip(t *testing.T) {
	bin := buildFixture(t)
	hub := mcpgate.NewHub()
	hub.ConnectAll(context.Background(), fixtureConfigs(bin))
	defer hub.Close(context.Background())

	var echoTool *mcpgate.McpTool
	for _, tl := range hub.Tools() {
		if tl.Name == "echo" {
			t := tl
			echoTool = &t
		}
	}
	if echoTool == nil {
		t.Fatal("echo tool not found")
	}

	harnessTool := mcpgate.ToHarnessTool(hub, *echoTool)
	if harnessTool.Name != "mcp__fixture__echo" {
		t.Fatalf("tool name = %q", harnessTool.Name)
	}

	args, _ := json.Marshal(map[string]any{"text": "hello fixture"})
	res, err := harnessTool.Execute(context.Background(), args, nil, tool.Invocation{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error result: %+v", res.Content)
	}
	if got := msg.TextOf(res.Content); got != "hello fixture" {
		t.Fatalf("got %q, want %q", got, "hello fixture")
	}
}

func TestToHarnessToolFail(t *testing.T) {
	bin := buildFixture(t)
	hub := mcpgate.NewHub()
	hub.ConnectAll(context.Background(), fixtureConfigs(bin))
	defer hub.Close(context.Background())

	var failTool *mcpgate.McpTool
	for _, tl := range hub.Tools() {
		if tl.Name == "fail" {
			tt := tl
			failTool = &tt
		}
	}
	if failTool == nil {
		t.Fatal("fail tool not found")
	}

	harnessTool := mcpgate.ToHarnessTool(hub, *failTool)
	args, _ := json.Marshal(map[string]any{"message": "boom"})
	res, err := harnessTool.Execute(context.Background(), args, nil, tool.Invocation{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError, got %+v", res)
	}
	if !strings.Contains(msg.TextOf(res.Content), "boom") {
		t.Fatalf("expected error content to mention boom, got %+v", res.Content)
	}
}

func TestToHarnessToolBigContentLength(t *testing.T) {
	bin := buildFixture(t)
	hub := mcpgate.NewHub()
	hub.ConnectAll(context.Background(), fixtureConfigs(bin))
	defer hub.Close(context.Background())

	var bigTool *mcpgate.McpTool
	for _, tl := range hub.Tools() {
		if tl.Name == "big" {
			tt := tl
			bigTool = &tt
		}
	}
	if bigTool == nil {
		t.Fatal("big tool not found")
	}

	harnessTool := mcpgate.ToHarnessTool(hub, *bigTool)
	args, _ := json.Marshal(map[string]any{"lines": 5})
	res, err := harnessTool.Execute(context.Background(), args, nil, tool.Invocation{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	text := msg.TextOf(res.Content)
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d: %q", len(lines), text)
	}
}

func TestConnectAllFailStartupOtherServersStillConnect(t *testing.T) {
	bin := buildFixture(t)
	configs := map[string]mcpgate.ServerConfig{
		"dead": {Command: bin, Args: []string{"--fail-startup"}},
		"good": {Command: bin},
	}
	hub := mcpgate.NewHub()
	hub.ConnectAll(context.Background(), configs)
	defer hub.Close(context.Background())

	statuses := map[string]mcpgate.ServerStatus{}
	for _, s := range hub.Statuses() {
		statuses[s.Name] = s
	}
	dead, ok := statuses["dead"]
	if !ok || dead.OK || dead.Error == "" {
		t.Fatalf("expected dead server to fail with an error, got %+v", dead)
	}
	good, ok := statuses["good"]
	if !ok || !good.OK || good.ToolCount != 4 {
		t.Fatalf("expected good server to connect despite dead sibling, got %+v", good)
	}
}

func TestConnectAllHangTimesOutAndCleansUpProcess(t *testing.T) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not available")
	}
	bin := buildFixture(t)
	t.Setenv("HARNESS_MCP_CONNECT_TIMEOUT", "1s")

	configs := map[string]mcpgate.ServerConfig{
		"hung": {Command: bin, Args: []string{"--hang"}},
	}
	hub := mcpgate.NewHub()

	start := time.Now()
	done := make(chan struct{})
	go func() {
		hub.ConnectAll(context.Background(), configs)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("ConnectAll did not return: goroutine leak or missing timeout")
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("ConnectAll against a hung server took %v, want well under 15s", elapsed)
	}

	statuses := hub.Statuses()
	if len(statuses) != 1 || statuses[0].OK {
		t.Fatalf("expected hung server to be recorded as failed, got %+v", statuses)
	}
	if statuses[0].Error == "" {
		t.Fatal("expected a recorded error for the hung server")
	}

	hub.Close(context.Background())

	// No leftover mcpfixture --hang process: ConnectAll's failure path (and
	// go-sdk's own Client.Connect error path) must have killed it already.
	time.Sleep(200 * time.Millisecond)
	out, _ := exec.Command("pgrep", "-f", bin+" --hang").Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("expected no leftover mcpfixture --hang process, pgrep found: %q", out)
	}
}

func TestClaudeJSONFixtureRoundTrip(t *testing.T) {
	bin := buildFixture(t)
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(mcpfixture.ClaudeJSON(bin)), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	cfgs := mcpgate.ResolveConfigs("", false)
	if len(cfgs) != 1 {
		t.Fatalf("expected 1 server from ~/.claude.json, got %+v", cfgs)
	}
	fixture, ok := cfgs["fixture"]
	if !ok || fixture.Command != bin {
		t.Fatalf("fixture config = %+v", fixture)
	}
}
