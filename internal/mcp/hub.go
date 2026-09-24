package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"sync"
	"syscall"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
)

// defaultConnectTimeout is CONNECT_TIMEOUT_MS in client.ts.
const defaultConnectTimeout = 30 * time.Second

// connectTimeoutEnv overrides the per-server connect/list-tools timeout, for
// tests that need a fast, deterministic timeout against a hung server.
const connectTimeoutEnv = "HARNESS_MCP_CONNECT_TIMEOUT"

func connectTimeout() time.Duration {
	if v := os.Getenv(connectTimeoutEnv); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultConnectTimeout
}

// McpTool is one tool advertised by a connected MCP server. Mirrors
// client.ts's McpTool.
type McpTool struct {
	Server string
	Name   string
	// QualifiedName matches Claude's `mcp__server__tool` convention.
	QualifiedName string
	Description   string
	InputSchema   json.RawMessage
}

// ServerStatus is the outcome of connecting to one configured server.
// Mirrors client.ts's ServerStatus.
type ServerStatus struct {
	Name      string
	OK        bool
	Error     string
	ToolCount int
	Ms        int64
}

// serverConn is what Hub keeps per connected server: the live session, and
// (for stdio servers) the command that was started, so Close can reach the
// whole process group rather than only the direct child.
type serverConn struct {
	session *sdk.ClientSession
	cmd     *exec.Cmd
}

// Hub connects to every configured MCP server and adapts their tools.
// Mirrors client.ts's McpHub.
type Hub struct {
	mu    sync.Mutex
	conns map[string]*serverConn

	tools    []McpTool
	statuses []ServerStatus
}

// NewHub builds an empty, unconnected Hub.
func NewHub() *Hub {
	return &Hub{conns: map[string]*serverConn{}}
}

// Tools returns every tool of every server that connected successfully.
func (h *Hub) Tools() []McpTool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]McpTool(nil), h.tools...)
}

// Statuses returns the connect outcome of every configured server, in the
// order they were attempted.
func (h *Hub) Statuses() []ServerStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]ServerStatus(nil), h.statuses...)
}

// ConnectAll connects to every configured server and collects their
// catalogs.
//
// Sequential rather than parallel: several of these spawn `uvx`/`npx` child
// processes, and a stampede of cold starts muddies both timing and failure
// messages (see client.ts's connectAll comment). A server that fails is
// recorded and skipped — one broken entry must not deny every other server.
//
// Go map iteration order is not deterministic, unlike the TS
// Object.entries used by client.ts, so servers are attempted in name-sorted
// order for reproducible test output; this has no effect on the outcome,
// since each server is independent.
func (h *Hub) ConnectAll(ctx context.Context, configs map[string]ServerConfig) {
	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, name)
	}
	sort.Strings(names)

	timeout := connectTimeout()
	for _, name := range names {
		cfg := configs[name]
		started := time.Now()

		transport, cmd, err := buildTransport(cfg)
		if err != nil {
			h.mu.Lock()
			h.statuses = append(h.statuses, ServerStatus{Name: name, OK: false, Error: err.Error(), Ms: elapsedMs(started)})
			h.mu.Unlock()
			continue
		}

		client := sdk.NewClient(&sdk.Implementation{Name: "harness", Version: "0.0.0"}, nil)

		connectCtx, cancel := context.WithTimeout(ctx, timeout)
		session, err := client.Connect(connectCtx, transport, nil)
		cancel()
		if err != nil {
			// Client.Connect already closes the session (and, for stdio, the
			// child process) on its own error paths; killGroup is a defensive
			// second pass for a process group's other members (e.g. `npx`
			// spawning `node`), which the SDK's Close only ever reaches for
			// the direct child.
			killGroup(cmd)
			h.mu.Lock()
			h.statuses = append(h.statuses, ServerStatus{
				Name: name, OK: false, Error: fmt.Sprintf("%s connect timed out or failed: %s", name, err), Ms: elapsedMs(started),
			})
			h.mu.Unlock()
			continue
		}

		listCtx, listCancel := context.WithTimeout(ctx, timeout)
		res, err := session.ListTools(listCtx, nil)
		listCancel()
		if err != nil {
			_ = session.Close()
			killGroup(cmd)
			h.mu.Lock()
			h.statuses = append(h.statuses, ServerStatus{
				Name: name, OK: false, Error: fmt.Sprintf("%s listTools timed out or failed: %s", name, err), Ms: elapsedMs(started),
			})
			h.mu.Unlock()
			continue
		}

		var tools []McpTool
		for _, t := range res.Tools {
			schema, _ := json.Marshal(t.InputSchema)
			tools = append(tools, McpTool{
				Server:        name,
				Name:          t.Name,
				QualifiedName: fmt.Sprintf("mcp__%s__%s", name, t.Name),
				Description:   t.Description,
				InputSchema:   schema,
			})
		}

		h.mu.Lock()
		h.conns[name] = &serverConn{session: session, cmd: cmd}
		h.tools = append(h.tools, tools...)
		h.statuses = append(h.statuses, ServerStatus{Name: name, OK: true, ToolCount: len(tools), Ms: elapsedMs(started)})
		h.mu.Unlock()
	}
}

func elapsedMs(started time.Time) int64 { return time.Since(started).Milliseconds() }

// Call invokes one MCP tool by its qualified name.
func (h *Hub) Call(ctx context.Context, qualifiedName string, args map[string]any) (*sdk.CallToolResult, error) {
	h.mu.Lock()
	var found *McpTool
	for i := range h.tools {
		if h.tools[i].QualifiedName == qualifiedName {
			found = &h.tools[i]
			break
		}
	}
	var conn *serverConn
	if found != nil {
		conn = h.conns[found.Server]
	}
	h.mu.Unlock()

	if found == nil {
		return nil, fmt.Errorf("unknown MCP tool: %s", qualifiedName)
	}
	if conn == nil || conn.session == nil {
		return nil, fmt.Errorf("server not connected: %s", found.Server)
	}
	return conn.session.CallTool(ctx, &sdk.CallToolParams{Name: found.Name, Arguments: args})
}

// Close closes every connected server. For stdio servers this also kills
// the process group Setpgid placed the child in, so descendants the child
// spawned (uvx/npx cold-starting a further node/python process) do not
// outlive the session.
func (h *Hub) Close(ctx context.Context) {
	h.mu.Lock()
	conns := make([]*serverConn, 0, len(h.conns))
	for _, c := range h.conns {
		conns = append(conns, c)
	}
	h.conns = map[string]*serverConn{}
	h.mu.Unlock()

	var wg sync.WaitGroup
	for _, c := range conns {
		wg.Add(1)
		go func(c *serverConn) {
			defer wg.Done()
			if c.session != nil {
				_ = c.session.Close()
			}
			killGroup(c.cmd)
		}(c)
	}
	wg.Wait()
}

// buildTransport builds the SDK transport cfg implies. For stdio it also
// returns the *exec.Cmd, started with Setpgid so the whole process group
// (not just the direct child) can be killed on Close or on a failed
// connect. stderr is discarded (nil connects to /dev/null), matching
// client.ts's `stderr: "ignore"`; env is the process environment plus
// cfg.Env, since children shell out to uv/npx/tsx and need the real PATH.
func buildTransport(cfg ServerConfig) (sdk.Transport, *exec.Cmd, error) {
	switch TransportType(cfg) {
	case "http", "sse":
		if cfg.URL == "" {
			return nil, nil, fmt.Errorf("mcp server: http transport requires a url")
		}
		httpClient := &http.Client{Transport: &headerTransport{headers: cfg.Headers}}
		return &sdk.StreamableClientTransport{Endpoint: cfg.URL, HTTPClient: httpClient}, nil, nil
	default:
		if cfg.Command == "" {
			return nil, nil, fmt.Errorf("mcp server: stdio transport requires a command")
		}
		cmd := exec.Command(cfg.Command, cfg.Args...)
		cmd.Env = mergeEnv(os.Environ(), cfg.Env)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		timeout := connectTimeout()
		return &sdk.CommandTransport{Command: cmd, TerminateDuration: timeout}, cmd, nil
	}
}

// mergeEnv appends overrides onto base as "KEY=VALUE" pairs. A later
// duplicate key wins, which is how os/exec (and the OS underneath it)
// resolves duplicate entries.
func mergeEnv(base []string, overrides map[string]string) []string {
	env := append([]string(nil), base...)
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	return env
}

// killGroup sends SIGKILL to cmd's whole process group (its pid, since
// Setpgid made it the group leader) and does not wait for it: the direct
// child is already reaped by whichever of session.Close or the SDK's own
// error-path Close ran first, and waiting on cmd a second time would panic.
// A nonexistent group (already dead) is silently ignored.
func killGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// headerTransport is an http.RoundTripper that adds fixed headers to every
// request, standing in for the TS SDK's `requestInit: { headers }` option,
// which the Go SDK's StreamableClientTransport has no equivalent field for.
type headerTransport struct {
	headers map[string]string
	base    http.RoundTripper
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// ToHarnessTool adapts an MCP tool into harness's tool.Tool: Execute calls
// hub.Call and maps the result's content blocks into tool.Result. Mirrors
// client.ts's toHarnessTool.
func ToHarnessTool(hub *Hub, t McpTool) *tool.Tool {
	schema := t.InputSchema
	if len(schema) == 0 || string(schema) == "null" {
		schema = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return &tool.Tool{
		Name:        t.QualifiedName,
		Label:       fmt.Sprintf("%s: %s", t.Server, t.Name),
		Description: t.Description,
		Parameters:  schema,
		Execute: func(ctx context.Context, args json.RawMessage, onUpdate tool.Update, inv tool.Invocation) (tool.Result, error) {
			var params map[string]any
			if len(args) > 0 {
				if err := json.Unmarshal(args, &params); err != nil {
					return tool.Result{}, fmt.Errorf("mcp tool %s: decoding arguments: %w", t.QualifiedName, err)
				}
			}
			if params == nil {
				params = map[string]any{}
			}
			res, err := hub.Call(ctx, t.QualifiedName, params)
			if err != nil {
				return tool.Result{}, err
			}
			var blocks msg.Blocks
			for _, c := range res.Content {
				switch v := c.(type) {
				case *sdk.TextContent:
					blocks = append(blocks, msg.Text(v.Text))
				case *sdk.ImageContent:
					blocks = append(blocks, msg.Image(v.MIMEType, base64.StdEncoding.EncodeToString(v.Data)))
				}
			}
			return tool.Result{Content: blocks, IsError: res.IsError}, nil
		},
	}
}
