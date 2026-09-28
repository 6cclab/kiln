package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"

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

	// Scope is where the entry came from (ScopeUser, ScopeLocal,
	// ScopeProject or ScopeFlag); set by Resolve, never read from or
	// written to a config file.
	Scope string `json:"-"`
}

// Configuration scopes, the same three Claude Code uses (`claude mcp add
// --scope`), plus the --mcp-config file.
const (
	// ScopeLocal is private to the user and one project: ~/.claude.json's
	// projects[<abs path>].mcpServers. The default for `mcp add`.
	ScopeLocal = "local"
	// ScopeProject is shared through the repo: <project>/.mcp.json.
	ScopeProject = "project"
	// ScopeUser is every project for this user: ~/.claude.json's
	// top-level mcpServers.
	ScopeUser = "user"
	// ScopeFlag is the file passed with --mcp-config.
	ScopeFlag = "flag"
)

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

// ResolveOptions is what Resolve reads configuration for.
type ResolveOptions struct {
	// Cwd is the project directory: it selects the local-scope entry in
	// ~/.claude.json and is where .mcp.json is looked for.
	Cwd string
	// Path is --mcp-config ("" when not given); Strict is
	// --strict-mcp-config.
	Path   string
	Strict bool
}

// Resolved is every configured server, split by whether it may start now.
// Project servers come from a file in the repository — a command a clone
// can ship — so they only start once the folder is trusted; the caller
// decides that and connects Project then.
type Resolved struct {
	Servers map[string]ServerConfig // user, local and --mcp-config entries
	Project map[string]ServerConfig // .mcp.json entries not shadowed by the above
	// ProjectFile is the .mcp.json that was read ("" when none).
	ProjectFile string
}

// All is Servers and Project together.
func (r Resolved) All() map[string]ServerConfig {
	out := make(map[string]ServerConfig, len(r.Servers)+len(r.Project))
	for k, v := range r.Project {
		out[k] = v
	}
	for k, v := range r.Servers {
		out[k] = v
	}
	return out
}

// Resolve reads every MCP server the way Claude Code does, so servers a
// user added with `claude mcp add` (any scope) or checked in as .mcp.json
// work in kiln unchanged. Precedence on a name clash: --mcp-config, then
// local, then project, then user. --strict-mcp-config uses only the
// --mcp-config file (nothing when none is given). ${VAR} and
// ${VAR:-default} in commands, arguments, env values, URLs and headers are
// expanded from the environment, as in Claude Code's .mcp.json.
func Resolve(opts ResolveOptions) Resolved {
	out := Resolved{Servers: map[string]ServerConfig{}, Project: map[string]ServerConfig{}}
	add := func(dst map[string]ServerConfig, servers map[string]ServerConfig, scope string) {
		for name, cfg := range servers {
			cfg.Scope = scope
			dst[name] = expandConfig(cfg)
		}
	}
	if opts.Strict {
		if opts.Path != "" {
			add(out.Servers, ReadServerConfigs(opts.Path), ScopeFlag)
		}
		return out
	}
	doc := readClaudeJSON(paths.ClaudeJSONPath())
	add(out.Servers, doc.MCPServers, ScopeUser)
	projectFile := FindProjectFile(opts.Cwd)
	if projectFile != "" {
		out.ProjectFile = projectFile
		add(out.Project, readMCPJSON(projectFile), ScopeProject)
	}
	add(out.Servers, doc.localServers(opts.Cwd), ScopeLocal)
	if opts.Path != "" {
		add(out.Servers, ReadServerConfigs(opts.Path), ScopeFlag)
	}
	for name, cfg := range out.Servers {
		if cfg.Scope == ScopeUser {
			if _, shadowed := out.Project[name]; shadowed {
				delete(out.Servers, name) // project beats user
			}
		} else {
			delete(out.Project, name) // local and --mcp-config beat project
		}
	}
	return out
}

// FindProjectFile is the .mcp.json that applies to cwd: in cwd, or the
// nearest parent up to the repository root (the first directory holding
// .git). "" when there is none.
func FindProjectFile(cwd string) string {
	if cwd == "" {
		return ""
	}
	dir := filepath.Clean(cwd)
	for {
		candidate := filepath.Join(dir, ".mcp.json")
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// claudeJSONDoc is the part of ~/.claude.json Resolve reads.
type claudeJSONDoc struct {
	MCPServers map[string]ServerConfig `json:"mcpServers"`
	Projects   map[string]struct {
		MCPServers map[string]ServerConfig `json:"mcpServers"`
	} `json:"projects"`
}

func readClaudeJSON(path string) claudeJSONDoc {
	var doc claudeJSONDoc
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &doc)
	}
	return doc
}

// localServers is the projects[<cwd>] entry, matched on the path as given
// or with symlinks resolved (macOS's /tmp is /private/tmp).
func (d claudeJSONDoc) localServers(cwd string) map[string]ServerConfig {
	if cwd == "" {
		return nil
	}
	if p, ok := d.Projects[cwd]; ok {
		return p.MCPServers
	}
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		if p, ok := d.Projects[real]; ok {
			return p.MCPServers
		}
	}
	return nil
}

func readMCPJSON(path string) map[string]ServerConfig {
	var doc struct {
		MCPServers map[string]ServerConfig `json:"mcpServers"`
	}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &doc)
	}
	return doc.MCPServers
}

// envRef matches ${VAR} and ${VAR:-default}.
var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

func expandString(s string) string {
	return envRef.ReplaceAllStringFunc(s, func(m string) string {
		g := envRef.FindStringSubmatch(m)
		if v, ok := os.LookupEnv(g[1]); ok && v != "" {
			return v
		}
		if g[2] != "" {
			return g[3]
		}
		return ""
	})
}

func expandConfig(cfg ServerConfig) ServerConfig {
	cfg.Command = expandString(cfg.Command)
	cfg.URL = expandString(cfg.URL)
	if cfg.Args != nil {
		args := make([]string, len(cfg.Args))
		for i, a := range cfg.Args {
			args[i] = expandString(a)
		}
		cfg.Args = args
	}
	expandMap := func(m map[string]string) map[string]string {
		if m == nil {
			return nil
		}
		out := make(map[string]string, len(m))
		for k, v := range m {
			out[k] = expandString(v)
		}
		return out
	}
	cfg.Env = expandMap(cfg.Env)
	cfg.Headers = expandMap(cfg.Headers)
	return cfg
}

// ServerIn reports whether scope's own file configures name (shadowed or
// not), for `mcp remove` without --scope.
func ServerIn(scope, cwd, name string) bool {
	var servers map[string]ServerConfig
	switch scope {
	case ScopeProject:
		servers = readMCPJSON(filepath.Join(cwd, ".mcp.json"))
	case ScopeUser:
		servers = readClaudeJSON(paths.ClaudeJSONPath()).MCPServers
	case ScopeLocal:
		servers = readClaudeJSON(paths.ClaudeJSONPath()).localServers(cwd)
	}
	_, ok := servers[name]
	return ok
}

// SortedNames is the map's keys in order.
func SortedNames(m map[string]ServerConfig) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
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
