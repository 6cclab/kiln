// Package cli, this file: `kiln mcp add|add-json|remove|list|get`, the
// Claude Code-compatible way to wire MCP servers from the shell. Entries
// land in the same files `claude mcp` writes (see internal/mcp's
// AddServer), so a server added with either tool works in both.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/andrepato/harness/internal/claude/trust"
	mcpgate "github.com/andrepato/harness/internal/mcp"
)

// MCPSubcommands are the `kiln mcp <sub>` names MCPCommand handles. They
// are dispatched before the global flag parser, which would reject their
// own flags (-s, -e, -H, --).
var MCPSubcommands = map[string]bool{"add": true, "add-json": true, "remove": true, "list": true, "get": true}

const mcpUsage = `usage:
  kiln mcp add [-s local|project|user] [-t stdio|http|sse] [-e KEY=VALUE]... [-H "Name: value"]...
               <name> <command-or-url> [args...]      (or: <name> -- <command> [args...])
  kiln mcp add-json [-s scope] <name> '<json>'
  kiln mcp remove [-s scope] <name>
  kiln mcp list
  kiln mcp get <name>

scopes: local (default; this project, only you, in ~/.claude.json), project (.mcp.json, shared
through the repo), user (every project, ~/.claude.json)`

// MCPCommand runs one `kiln mcp <sub>` command; argv starts at <sub>.
func MCPCommand(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "kiln mcp:", err)
		return 1
	}
	if len(argv) == 0 {
		fmt.Fprintln(stderr, mcpUsage)
		return 1
	}
	switch argv[0] {
	case "add":
		return mcpAdd(argv[1:], cwd, stdout, stderr)
	case "add-json":
		return mcpAddJSON(argv[1:], cwd, stdout, stderr)
	case "remove":
		return mcpRemove(argv[1:], cwd, stdout, stderr)
	case "list":
		return mcpList(ctx, cwd, stdout)
	case "get":
		return mcpGet(argv[1:], cwd, stdout, stderr)
	}
	fmt.Fprintln(stderr, mcpUsage)
	return 1
}

// mcpFlags is the parsed option set shared by add/add-json/remove.
type mcpFlags struct {
	scope     string
	transport string
	env       map[string]string
	headers   map[string]string
	pos       []string // positionals before "--"
	rest      []string // everything after "--"
	dashdash  bool
}

func parseMCPFlags(argv []string) (mcpFlags, error) {
	f := mcpFlags{env: map[string]string{}, headers: map[string]string{}}
	value := func(i *int, flag string) (string, error) {
		if *i+1 >= len(argv) {
			return "", fmt.Errorf("%s needs a value", flag)
		}
		*i++
		return argv[*i], nil
	}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if a == "--" {
			f.dashdash = true
			f.rest = argv[i+1:]
			break
		}
		// Once the command has started (name + command-or-url seen), the
		// remaining words are its arguments, flags included: `kiln mcp add
		// x npx -y pkg` passes -y to npx.
		if len(f.pos) >= 2 {
			f.pos = append(f.pos, a)
			continue
		}
		name, inline, hasInline := strings.Cut(a, "=")
		get := func() (string, error) {
			if hasInline {
				return inline, nil
			}
			return value(&i, name)
		}
		var err error
		switch name {
		case "-s", "--scope":
			f.scope, err = get()
		case "-t", "--transport":
			f.transport, err = get()
		case "-e", "--env":
			var kv string
			if kv, err = get(); err == nil {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || k == "" {
					err = fmt.Errorf("-e wants KEY=VALUE, got %q", kv)
				}
				f.env[k] = v
			}
		case "-H", "--header":
			var h string
			if h, err = get(); err == nil {
				k, v, ok := strings.Cut(h, ":")
				if !ok || strings.TrimSpace(k) == "" {
					err = fmt.Errorf("-H wants \"Name: value\", got %q", h)
				}
				f.headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		default:
			if strings.HasPrefix(a, "-") && len(a) > 1 {
				return f, fmt.Errorf("unknown option %s", a)
			}
			f.pos = append(f.pos, a)
		}
		if err != nil {
			return f, err
		}
	}
	switch f.scope {
	case "":
	case mcpgate.ScopeLocal, mcpgate.ScopeProject, mcpgate.ScopeUser:
	default:
		return f, fmt.Errorf("unknown scope %q (want local, project or user)", f.scope)
	}
	switch f.transport {
	case "", "stdio", "http", "sse":
	default:
		return f, fmt.Errorf("unknown transport %q (want stdio, http or sse)", f.transport)
	}
	return f, nil
}

func mcpAdd(argv []string, cwd string, stdout, stderr io.Writer) int {
	f, err := parseMCPFlags(argv)
	if err != nil {
		fmt.Fprintf(stderr, "kiln mcp add: %s\n\n%s\n", err, mcpUsage)
		return 1
	}
	var name string
	var target []string
	switch {
	case f.dashdash && len(f.pos) == 1:
		name, target = f.pos[0], f.rest
	case !f.dashdash && len(f.pos) >= 2:
		name, target = f.pos[0], f.pos[1:]
	}
	if name == "" || len(target) == 0 {
		fmt.Fprintf(stderr, "kiln mcp add: need a name and a command or URL\n\n%s\n", mcpUsage)
		return 1
	}
	cfg := mcpgate.ServerConfig{}
	switch f.transport {
	case "http", "sse":
		if len(target) != 1 {
			fmt.Fprintf(stderr, "kiln mcp add: a %s server takes one URL\n", f.transport)
			return 1
		}
		cfg.Type, cfg.URL = f.transport, target[0]
	default:
		if strings.Contains(target[0], "://") && f.transport == "" {
			fmt.Fprintf(stderr, "kiln mcp add: %s looks like a URL; add -t http (or -t sse) to connect to it\n", target[0])
			return 1
		}
		cfg.Command, cfg.Args = target[0], target[1:]
		if f.transport == "stdio" {
			cfg.Type = "stdio"
		}
	}
	if len(f.env) > 0 {
		cfg.Env = f.env
	}
	if len(f.headers) > 0 {
		cfg.Headers = f.headers
	}
	return writeServer(f.scope, cwd, name, cfg, stdout, stderr)
}

func mcpAddJSON(argv []string, cwd string, stdout, stderr io.Writer) int {
	f, err := parseMCPFlags(argv)
	if err != nil || len(f.pos) != 2 {
		if err == nil {
			err = errors.New("need a name and a JSON object")
		}
		fmt.Fprintf(stderr, "kiln mcp add-json: %s\n\n%s\n", err, mcpUsage)
		return 1
	}
	var cfg mcpgate.ServerConfig
	dec := json.NewDecoder(strings.NewReader(f.pos[1]))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		fmt.Fprintf(stderr, "kiln mcp add-json: not a server config: %s\n", err)
		return 1
	}
	if cfg.Command == "" && cfg.URL == "" {
		fmt.Fprintln(stderr, "kiln mcp add-json: the config needs a \"command\" or a \"url\"")
		return 1
	}
	return writeServer(f.scope, cwd, f.pos[0], cfg, stdout, stderr)
}

func writeServer(scope, cwd, name string, cfg mcpgate.ServerConfig, stdout, stderr io.Writer) int {
	if scope == "" {
		scope = mcpgate.ScopeLocal
	}
	path, err := mcpgate.AddServer(scope, cwd, name, cfg)
	if err != nil {
		fmt.Fprintln(stderr, "kiln mcp add:", err)
		return 1
	}
	fmt.Fprintf(stdout, "Added %s MCP server %s (%s) to %s\n", describeTransport(cfg), name, scope, path)
	if scope == mcpgate.ScopeProject {
		fmt.Fprintln(stdout, "It starts in kiln sessions once this folder is trusted.")
	}
	return 0
}

func describeTransport(cfg mcpgate.ServerConfig) string {
	return mcpgate.TransportType(cfg)
}

func mcpRemove(argv []string, cwd string, stdout, stderr io.Writer) int {
	f, err := parseMCPFlags(argv)
	if err != nil || len(f.pos) != 1 {
		if err == nil {
			err = errors.New("need the server's name")
		}
		fmt.Fprintf(stderr, "kiln mcp remove: %s\n\n%s\n", err, mcpUsage)
		return 1
	}
	name := f.pos[0]
	scopes := []string{f.scope}
	if f.scope == "" {
		// Like `claude mcp remove`: without --scope, remove it from the one
		// scope that has it, and refuse to guess between several.
		scopes = scopesWith(name, cwd)
		if len(scopes) == 0 {
			fmt.Fprintf(stderr, "kiln mcp remove: no MCP server named %s\n", name)
			return 1
		}
		if len(scopes) > 1 {
			fmt.Fprintf(stderr, "kiln mcp remove: %s is configured in several scopes (%s); pass -s to pick one\n", name, strings.Join(scopes, ", "))
			return 1
		}
	}
	path, err := mcpgate.RemoveServer(scopes[0], cwd, name)
	if errors.Is(err, mcpgate.ErrNotFound) {
		fmt.Fprintf(stderr, "kiln mcp remove: no MCP server named %s in %s scope\n", name, scopes[0])
		return 1
	}
	if err != nil {
		fmt.Fprintln(stderr, "kiln mcp remove:", err)
		return 1
	}
	fmt.Fprintf(stdout, "Removed MCP server %s (%s) from %s\n", name, scopes[0], path)
	return 0
}

// scopesWith lists every scope that configures name, shadowed or not.
func scopesWith(name, cwd string) []string {
	var out []string
	for _, scope := range []string{mcpgate.ScopeLocal, mcpgate.ScopeProject, mcpgate.ScopeUser} {
		if mcpgate.ServerIn(scope, cwd, name) {
			out = append(out, scope)
		}
	}
	return out
}

func mcpList(ctx context.Context, cwd string, stdout io.Writer) int {
	r := mcpgate.Resolve(mcpgate.ResolveOptions{Cwd: cwd})
	all := r.All()
	if len(all) == 0 {
		fmt.Fprintln(stdout, "No MCP servers configured. Add one with: kiln mcp add <name> -- <command> [args...]")
		return 0
	}
	trusted := folderTrusted(cwd)
	connect := r.Servers
	if trusted {
		connect = all
	}
	hub := mcpgate.NewHub()
	hub.ConnectAll(ctx, connect)
	defer hub.Close(ctx)
	status := map[string]mcpgate.ServerStatus{}
	for _, s := range hub.Statuses() {
		status[s.Name] = s
	}
	fmt.Fprintln(stdout, "Checking MCP server health…")
	fmt.Fprintln(stdout)
	for _, name := range mcpgate.SortedNames(all) {
		cfg := all[name]
		line := fmt.Sprintf("%s (%s): %s", name, cfg.Scope, targetOf(cfg))
		st, ok := status[name]
		switch {
		case !ok:
			line += " - not started: this folder is not trusted yet (start kiln here and trust it)"
		case st.OK:
			line += fmt.Sprintf(" - ✓ connected, %d tools", st.ToolCount)
		default:
			line += " - ✗ " + st.Error
		}
		fmt.Fprintln(stdout, line)
	}
	return 0
}

func mcpGet(argv []string, cwd string, stdout, stderr io.Writer) int {
	if len(argv) != 1 {
		fmt.Fprintf(stderr, "kiln mcp get: need the server's name\n\n%s\n", mcpUsage)
		return 1
	}
	all := mcpgate.Resolve(mcpgate.ResolveOptions{Cwd: cwd}).All()
	cfg, ok := all[argv[0]]
	if !ok {
		fmt.Fprintf(stderr, "kiln mcp get: no MCP server named %s\n", argv[0])
		return 1
	}
	fmt.Fprintf(stdout, "%s:\n  Scope: %s\n  Type: %s\n", argv[0], cfg.Scope, mcpgate.TransportType(cfg))
	if cfg.URL != "" {
		fmt.Fprintf(stdout, "  URL: %s\n", cfg.URL)
	} else {
		fmt.Fprintf(stdout, "  Command: %s\n  Args: %s\n", cfg.Command, strings.Join(cfg.Args, " "))
	}
	// Values can be secrets (tokens): name the keys only.
	for label, m := range map[string]map[string]string{"Environment": cfg.Env, "Headers": cfg.Headers} {
		if len(m) == 0 {
			continue
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(stdout, "  %s: %s (values hidden)\n", label, strings.Join(keys, ", "))
	}
	fmt.Fprintf(stdout, "\nTo remove it: kiln mcp remove %s -s %s\n", argv[0], cfg.Scope)
	return 0
}

func targetOf(cfg mcpgate.ServerConfig) string {
	if cfg.URL != "" {
		return cfg.URL + " (" + mcpgate.TransportType(cfg) + ")"
	}
	return strings.TrimSpace(cfg.Command + " " + strings.Join(cfg.Args, " "))
}

// folderTrusted reports whether project-scope servers may start in cwd.
func folderTrusted(cwd string) bool {
	if os.Getenv("HARNESS_TRUST_ALL") == "1" {
		return true
	}
	store, err := trust.NewStore()
	return err == nil && store.IsTrusted(cwd)
}
