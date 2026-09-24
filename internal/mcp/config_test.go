package mcp_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	mcpgate "github.com/andrepato/harness/internal/mcp"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadServerConfigsValid(t *testing.T) {
	path := writeConfig(t, `{"mcpServers":{"fixture":{"command":"/bin/fixture","args":["--foo"]},"http-one":{"url":"https://example.com/mcp","headers":{"X-Key":"abc"}}}}`)

	cfgs := mcpgate.ReadServerConfigs(path)
	if len(cfgs) != 2 {
		t.Fatalf("expected 2 servers, got %d: %+v", len(cfgs), cfgs)
	}
	fixture, ok := cfgs["fixture"]
	if !ok || fixture.Command != "/bin/fixture" || len(fixture.Args) != 1 || fixture.Args[0] != "--foo" {
		t.Errorf("fixture config = %+v", fixture)
	}
	if mcpgate.TransportType(fixture) != "stdio" {
		t.Errorf("expected stdio transport, got %q", mcpgate.TransportType(fixture))
	}

	httpOne := cfgs["http-one"]
	if httpOne.URL != "https://example.com/mcp" || httpOne.Headers["X-Key"] != "abc" {
		t.Errorf("http-one config = %+v", httpOne)
	}
	if mcpgate.TransportType(httpOne) != "http" {
		t.Errorf("expected inferred http transport, got %q", mcpgate.TransportType(httpOne))
	}
}

func TestReadServerConfigsMissingFile(t *testing.T) {
	cfgs := mcpgate.ReadServerConfigs(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if len(cfgs) != 0 {
		t.Fatalf("expected empty map for missing file, got %+v", cfgs)
	}
}

func TestReadServerConfigsInvalidJSON(t *testing.T) {
	path := writeConfig(t, `{not valid json`)
	cfgs := mcpgate.ReadServerConfigs(path)
	if len(cfgs) != 0 {
		t.Fatalf("expected empty map for invalid JSON, got %+v", cfgs)
	}
}

func TestReadServerConfigsNoMCPServersKey(t *testing.T) {
	path := writeConfig(t, `{"other":"stuff"}`)
	cfgs := mcpgate.ReadServerConfigs(path)
	if len(cfgs) != 0 {
		t.Fatalf("expected empty map, got %+v", cfgs)
	}
}

func TestResolveConfigsStrictWithoutFile(t *testing.T) {
	// This must NOT fall back to the default ~/.claude.json even if one
	// exists in the test's HOME, so point HOME at an empty temp dir too.
	t.Setenv("HOME", t.TempDir())
	cfgs := mcpgate.ResolveConfigs("", true)
	if len(cfgs) != 0 {
		t.Fatalf("expected empty map for strict mode with no file, got %+v", cfgs)
	}
}

func TestResolveConfigsStrictWithFile(t *testing.T) {
	path := writeConfig(t, `{"mcpServers":{"a":{"command":"x"}}}`)
	cfgs := mcpgate.ResolveConfigs(path, true)
	if len(cfgs) != 1 {
		t.Fatalf("expected 1 server, got %+v", cfgs)
	}
}

func TestResolveConfigsNonStrictReadsDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeJSON := filepath.Join(home, ".claude.json")
	body, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"a": map[string]any{"command": "x"}}})
	if err := os.WriteFile(claudeJSON, body, 0o644); err != nil {
		t.Fatal(err)
	}
	cfgs := mcpgate.ResolveConfigs("", false)
	if len(cfgs) != 1 {
		t.Fatalf("expected non-strict, no path to read ~/.claude.json, got %+v", cfgs)
	}
}
