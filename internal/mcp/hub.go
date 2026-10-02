package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/andrepato/harness/internal/diag"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
)

// defaultConnectTimeout is CONNECT_TIMEOUT_MS in client.ts.
const defaultConnectTimeout = 30 * time.Second

// terminateGrace is how long a stdio transport's Close waits for the
// child to exit on its own before killing it.
const terminateGrace = 500 * time.Millisecond

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
	Name string
	OK   bool
	// Error is a one-line, human-readable reason when OK is false
	// ("command not found: /path/tsx"), for the transcript notice, /mcp
	// and /doctor. Detail is the underlying error text in full.
	Error     string
	Detail    string
	ToolCount int
	Ms        int64
	// Scope is the configuration scope the server came from
	// (ServerConfig.Scope: user, local, project, flag), for /mcp's sections.
	Scope string
}

// describeConnectError turns the SDK's and exec's error chains into the
// one line a user can act on. The raw text is kept in ServerStatus.Detail.
func describeConnectError(cfg ServerConfig, err error) string {
	var pathErr *fs.PathError
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return fmt.Sprintf("command not found: %s", cfg.Command)
	case errors.As(err, &pathErr) && errors.Is(pathErr.Err, fs.ErrNotExist):
		return fmt.Sprintf("command not found: %s", pathErr.Path)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf("no response within %s", connectTimeout())
	}
	text := err.Error()
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "connection refused"):
		return "connection refused" + atURL(cfg)
	case strings.Contains(lower, "no such host"):
		return "host not found" + atURL(cfg)
	case strings.Contains(lower, "unauthorized") || strings.Contains(text, " 401"):
		return "unauthorized (check its token)" + atURL(cfg)
	case strings.Contains(lower, "forbidden") || strings.Contains(text, " 403"):
		return "forbidden" + atURL(cfg)
	case strings.Contains(lower, "eof") || strings.Contains(lower, "exited") || strings.Contains(lower, "broken pipe"):
		return "the server exited before it answered"
	}
	// Last resort: the innermost error, which is usually the useful one.
	inner := err
	for {
		next := errors.Unwrap(inner)
		if next == nil {
			break
		}
		inner = next
	}
	return strings.TrimSpace(inner.Error())
}

func atURL(cfg ServerConfig) string {
	if cfg.URL != "" {
		return " at " + cfg.URL
	}
	return ""
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

	tools    map[string][]McpTool // by server; Tools() orders them by server name
	statuses []ServerStatus
	scopes   map[string]string // server name -> ServerConfig.Scope
	pending  map[string]bool   // servers ConnectAll is still connecting

	// OnServer, if set before ConnectAll, is called after each server's
	// connect attempt finishes, so a UI can show progress while the
	// remaining servers connect. Called from ConnectAll's goroutine.
	OnServer func(ServerStatus)
}

// NewHub builds an empty, unconnected Hub.
func NewHub() *Hub {
	return &Hub{conns: map[string]*serverConn{}, tools: map[string][]McpTool{}, scopes: map[string]string{}, pending: map[string]bool{}}
}

// record appends a server's outcome, logs it, and reports it to OnServer.
func (h *Hub) record(st ServerStatus) {
	h.mu.Lock()
	st.Scope = h.scopes[st.Name]
	h.statuses = append(h.statuses, st)
	delete(h.pending, st.Name)
	cb := h.OnServer
	h.mu.Unlock()
	if st.OK {
		diag.L().Info("mcp connected", "server", st.Name, "tools", st.ToolCount, "ms", st.Ms)
	} else {
		diag.L().Warn("mcp failed", "server", st.Name, "reason", st.Error, "detail", st.Detail, "ms", st.Ms)
	}
	if cb != nil {
		cb(st)
	}
}

// Tools returns every tool of every server that connected successfully.
func (h *Hub) Tools() []McpTool {
	h.mu.Lock()
	defer h.mu.Unlock()
	servers := make([]string, 0, len(h.tools))
	for name := range h.tools {
		servers = append(servers, name)
	}
	sort.Strings(servers)
	var out []McpTool
	for _, name := range servers {
		out = append(out, h.tools[name]...)
	}
	return out
}

// Statuses returns the connect outcome of every configured server, in the
// order they were attempted.
func (h *Hub) Statuses() []ServerStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]ServerStatus(nil), h.statuses...)
}

// Pending returns the servers ConnectAll is still connecting, by name,
// with their scopes, so /mcp can list them before they finish.
func (h *Hub) Pending() []ServerStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]ServerStatus, 0, len(h.pending))
	for name := range h.pending {
		out = append(out, ServerStatus{Name: name, Scope: h.scopes[name]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
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
	h.mu.Lock()
	for name, cfg := range configs {
		names = append(names, name)
		h.scopes[name] = cfg.Scope
		h.pending[name] = true
	}
	h.mu.Unlock()
	sort.Strings(names)

	// Servers connect concurrently: one slow or dead server costs its own
	// timeout, not everyone's behind it.
	timeout := connectTimeout()
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.connectOne(ctx, name, configs[name], timeout)
		}()
	}
	wg.Wait()
}

// connectOne connects one server, lists its tools and records the outcome.
func (h *Hub) connectOne(ctx context.Context, name string, cfg ServerConfig, timeout time.Duration) {
	started := time.Now()

	transport, cmd, err := buildTransport(cfg)
	if err != nil {
		h.record(ServerStatus{Name: name, OK: false, Error: describeConnectError(cfg, err), Detail: err.Error(), Ms: elapsedMs(started)})
		return
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
		h.record(ServerStatus{
			Name: name, OK: false, Error: describeConnectError(cfg, err), Detail: fmt.Sprintf("connect: %s", err), Ms: elapsedMs(started),
		})
		return
	}

	listCtx, listCancel := context.WithTimeout(ctx, timeout)
	res, err := session.ListTools(listCtx, nil)
	listCancel()
	if err != nil {
		_ = session.Close()
		killGroup(cmd)
		h.record(ServerStatus{
			Name: name, OK: false, Error: "connected, but listing its tools failed: " + describeConnectError(cfg, err), Detail: fmt.Sprintf("listTools: %s", err), Ms: elapsedMs(started),
		})
		return
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
	h.tools[name] = tools
	h.mu.Unlock()
	h.record(ServerStatus{Name: name, OK: true, ToolCount: len(tools), Ms: elapsedMs(started)})
}

func elapsedMs(started time.Time) int64 { return time.Since(started).Milliseconds() }

// Call invokes one MCP tool by its qualified name.
func (h *Hub) Call(ctx context.Context, qualifiedName string, args map[string]any) (*sdk.CallToolResult, error) {
	h.mu.Lock()
	var found *McpTool
	for _, tools := range h.tools {
		for i := range tools {
			if tools[i].QualifiedName == qualifiedName {
				found = &tools[i]
				break
			}
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
		// TerminateDuration is how long Close waits after closing stdin
		// before it kills the child. It used to equal the connect timeout,
		// so a server that never answered cost two timeouts: one for the
		// handshake, one for a child that ignores stdin closing. A short
		// grace is enough; the failure path SIGKILLs the group anyway.
		return &sdk.CommandTransport{Command: cmd, TerminateDuration: terminateGrace}, cmd, nil
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
		// The server validates its own input; kiln only refuses keys the
		// gate and the server could read differently (tool.CheckArgs).
		PassThroughArgs: true,
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
