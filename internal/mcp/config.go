package mcp

import (
	"encoding/json"
	"os"

	"github.com/andrepato/harness/internal/claude/paths"
)

// ServerConfig is one entry under ~/.claude.json's "mcpServers" map. Field
// names mirror client.ts's McpServerConfig; "type" is absent on some real
// entries (proxmox, grafana) so the transport is inferred from shape rather
// than required.
type ServerConfig struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// TransportType returns the transport cfg implies: the explicit "type" if
// set, otherwise "http" when a URL is present, otherwise "stdio". Mirrors
// client.ts's `cfg.type ?? (cfg.url ? "http" : "stdio")`.
func TransportType(cfg ServerConfig) string {
	if cfg.Type != "" {
		return cfg.Type
	}
	if cfg.URL != "" {
		return "http"
	}
	return "stdio"
}

// claudeJSON is the shape of ~/.claude.json this package cares about.
type claudeJSON struct {
	MCPServers map[string]ServerConfig `json:"mcpServers"`
}

// ReadServerConfigs reads the "mcpServers" map from path, or from
// ~/.claude.json when path is empty. Any read or parse failure — missing
// file, invalid JSON, wrong shape — returns an empty map rather than an
// error, mirroring client.ts's readServerConfigs, which swallows every
// failure the same way: a broken or absent config must not prevent startup.
func ReadServerConfigs(path string) map[string]ServerConfig {
	if path == "" {
		path = paths.ClaudeJSONPath()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]ServerConfig{}
	}
	var cfg claudeJSON
	if err := json.Unmarshal(data, &cfg); err != nil {
		return map[string]ServerConfig{}
	}
	if cfg.MCPServers == nil {
		return map[string]ServerConfig{}
	}
	return cfg.MCPServers
}

// ResolveConfigs applies `--mcp-config <path>` / `--strict-mcp-config`
// semantics, ported from cli.ts:233-237:
//
//	argv.strictMcpConfig && !argv.mcpConfig ? {} : await readServerConfigs(argv.mcpConfig)
//
// path is the --mcp-config value ("" when not given). strict is
// --strict-mcp-config. Strict mode with no file given connects to nothing —
// it does NOT fall back to the default ~/.claude.json. Any other
// combination reads from path (or the default when path is empty).
func ResolveConfigs(path string, strict bool) map[string]ServerConfig {
	if strict && path == "" {
		return map[string]ServerConfig{}
	}
	return ReadServerConfigs(path)
}

// ConfigPath is the mcpServers file the harness reads: path when given,
// otherwise the default ~/.claude.json.
func ConfigPath(path string) string {
	if path != "" {
		return path
	}
	return paths.ClaudeJSONPath()
}
