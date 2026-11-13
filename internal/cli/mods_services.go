package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/automode"
	"github.com/andrepato/harness/internal/claude/agents"
	"github.com/andrepato/harness/internal/claude/permission"
	slashcommands "github.com/andrepato/harness/internal/commands"
	"github.com/andrepato/harness/internal/crash"
	"github.com/andrepato/harness/internal/diag"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/mcp"
	"github.com/andrepato/harness/internal/mods"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
)

// The tool, command and agent services behind $.tool.*, $.command.* and
// $.agent.register/spawn (see mods.Services).

// modsToolDeps is what those services reach: the main conversation's tools
// and lane, the permission gate, the command registry and the dispatcher.
type modsToolDeps struct {
	rt         *modsRuntime
	harness    *harness.Harness
	lane       automode.EntryLister
	gate       *permission.Gate
	dispatcher *agent.Dispatcher
	cwd        string
	// hub is the session's MCP hub ($.mcp.connect reads what it connected).
	hub *mcp.Hub
	// sandboxed is set when the OS sandbox wraps the bash tool; a mod's
	// process is not wrapped by it, so $.process.run and $.process.spawn refuse.
	sandboxed bool
}

// quoteArg is arg as one shell word, left bare when it has nothing to quote.
func quoteArg(arg string) string {
	if arg != "" && strings.Trim(arg, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-") == "" {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// modsProcessCommand is the shell command line the argument vector of
// $.process.run or $.process.spawn stands for, as the permission gate reads
// it: a directory other than the session's is a leading cd, env a leading
// assignment each.
func modsProcessCommand(argv []string, cwd, sessionCwd string, env map[string]string) string {
	var parts []string
	if cwd != "" && cwd != sessionCwd {
		parts = append(parts, "cd "+quoteArg(cwd)+" &&")
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, k+"="+quoteArg(env[k]))
	}
	for _, a := range argv {
		parts = append(parts, quoteArg(a))
	}
	return strings.Join(parts, " ")
}

// gateModProcess decides whether a mod's process may run: the permission gate
// judges it as the Bash call it amounts to (rules, modes, prompts, protected
// paths and auto mode all apply), and nothing runs when the gate cannot be
// asked. deny is the reason it may not.
func gateModProcess(ctx context.Context, set *tool.Set, gate *permission.Gate, lane automode.EntryLister, sandboxed bool,
	plugin string, argv []string, cwd, sessionCwd string, env map[string]string) (string, error) {
	if sandboxed {
		return "kiln's OS sandbox is on and does not wrap a mod's process, so " + plugin + " may not start one", nil
	}
	if gate == nil || set == nil {
		return "kiln has no permission gate to judge it by", nil
	}
	bash, _, ok := kilnToolName(set, "Bash")
	if !ok {
		return "kiln has no Bash tool to judge it as", nil
	}
	cmd := modsProcessCommand(argv, cwd, sessionCwd, env)
	req := permission.Request{ToolName: bash, PrimaryArg: cmd, Args: map[string]any{"command": cmd},
		History: autoModeHistory(ctx, lane)}
	blocked, _, err := gate.CheckWithOutcome(ctx, req)
	if err != nil {
		return "", err
	}
	if blocked != nil {
		return blocked.Reason, nil
	}
	return "", nil
}

// modsMcpConnect is what $.mcp.connect answers for plugin's server key. kiln
// connects every enabled plugin's MCP servers (hub name plugin_<plugin>_<key>)
// at session start and starts none later, so this reads what that came to.
func modsMcpConnect(statuses, pending []mcp.ServerStatus, plugin, server string) mods.McpConnectAnswer {
	name := fmt.Sprintf("plugin_%s_%s", plugin, server)
	for _, st := range statuses {
		if st.Name != name {
			continue
		}
		if st.OK {
			return mods.McpConnectAnswer{Connected: true, Server: name}
		}
		return mods.McpConnectAnswer{Reason: "failed", Message: fmt.Sprintf("%s did not connect: %s", server, st.Error)}
	}
	for _, st := range pending {
		if st.Name == name {
			return mods.McpConnectAnswer{Reason: "failed",
				Message: server + " is still connecting; kiln connects a plugin's servers at session start and starts none on demand"}
		}
	}
	return mods.McpConnectAnswer{Reason: "unlisted",
		Message: fmt.Sprintf("%s lists no MCP server %q that kiln connected; kiln starts none on demand", plugin, server)}
}

var modAgentSeq atomic.Int64

// kilnToolName finds the tool in set a mod names: kiln's own name, or Claude
// Code's spelling of one ("Bash" is kiln's bash, whichever of bash and
// bash_background comes first by exact lower-case match).
func kilnToolName(set *tool.Set, name string) (string, *tool.Tool, bool) {
	if t, ok := set.Get(name); ok {
		return name, t, true
	}
	pick := ""
	for _, n := range set.Names() {
		if claudeToolName(n) != name {
			continue
		}
		if pick == "" || strings.EqualFold(n, name) {
			pick = n
		}
	}
	if pick == "" {
		return "", nil, false
	}
	t, _ := set.Get(pick)
	return pick, t, true
}

func commandSource(o slashcommands.Origin) string {
	switch o {
	case slashcommands.OriginBuiltin:
		return "builtin"
	case slashcommands.OriginProject, slashcommands.OriginPersonal:
		return "user"
	}
	return "plugin" // plugin commands and skills
}

// fill sets the tool, command and agent services on s.
func (d modsToolDeps) fill(s *mods.Services) {
	s.GateProcess = func(ctx context.Context, plugin string, argv []string, cwd string, env map[string]string) (string, error) {
		var set *tool.Set
		if d.harness != nil {
			set = d.harness.Tools()
		}
		return gateModProcess(ctx, set, d.gate, d.lane, d.sandboxed, plugin, argv, cwd, d.cwd, env)
	}
	s.McpConnect = func(_ context.Context, plugin, server string) mods.McpConnectAnswer {
		if d.hub == nil {
			return mods.McpConnectAnswer{Reason: "failed", Message: "kiln has no MCP hub in this run"}
		}
		return modsMcpConnect(d.hub.Statuses(), d.hub.Pending(), plugin, server)
	}

	s.Config = func() []mods.ConfigRow {
		var model func() string
		if d.rt != nil {
			model = d.rt.modelFn
		}
		return modsConfigRows(model, d.gate)
	}

	s.Tools = func() []mods.ToolInfo {
		set := d.harness.Tools()
		var out []mods.ToolInfo
		seen := map[string]bool{}
		for _, n := range set.Names() {
			name := claudeToolName(n)
			if seen[name] {
				continue
			}
			seen[name] = true
			t, _ := set.Get(n)
			out = append(out, mods.ToolInfo{Name: name, Description: t.Description, MCP: strings.HasPrefix(n, "mcp__")})
		}
		return out
	}

	request := func(kn string, raw json.RawMessage, id string, ctx context.Context) (permission.Request, map[string]any, error) {
		var args map[string]any
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &args); err != nil {
				return permission.Request{}, nil, fmt.Errorf("the tool's input is not a JSON object: %w", err)
			}
		}
		if args == nil {
			args = map[string]any{}
		}
		primary, _ := permission.PrimaryArgOf(args)
		return permission.Request{ToolName: kn, PrimaryArg: primary, Args: args, CallID: id, History: autoModeHistory(ctx, d.lane)}, args, nil
	}

	s.CheckTool = func(ctx context.Context, name string, raw json.RawMessage) (mods.ToolVerdict, error) {
		kn, _, ok := kilnToolName(d.harness.Tools(), name)
		if !ok {
			kn = name
		}
		req, _, err := request(kn, raw, "", ctx)
		if err != nil {
			return mods.ToolVerdict{}, err
		}
		v, err := d.gate.Query(ctx, req)
		return mods.ToolVerdict{Decision: v.Decision, Reason: v.Reason}, err
	}

	// CallTool runs the call through the permission gate, which may prompt the
	// person, then the tool. The settings PreToolUse hooks do not run for it.
	s.CallTool = func(ctx context.Context, name string, raw json.RawMessage, id string) (mods.ToolOutput, error) {
		kn, t, ok := kilnToolName(d.harness.Tools(), name)
		if !ok {
			return mods.ToolOutput{}, fmt.Errorf("no tool named %q", name)
		}
		if d.rt.isModTool(kn) {
			return mods.ToolOutput{Deny: "no tool.call hook answered " + name + ": a mod's tool runs nothing of its own"}, nil
		}
		req, args, err := request(kn, raw, id, ctx)
		if err != nil {
			return mods.ToolOutput{}, err
		}
		if err := d.harness.CheckToolArgs(kn, args); err != nil {
			return mods.ToolOutput{Text: err.Error(), IsError: true}, nil
		}
		blocked, _, err := d.gate.CheckWithOutcome(ctx, req)
		if err != nil {
			return mods.ToolOutput{}, err
		}
		if blocked != nil {
			return mods.ToolOutput{Deny: blocked.Reason}, nil
		}
		res, err := t.Execute(ctx, raw, func(tool.Result) {}, tool.Invocation{ToolCallID: id, ToolName: kn, Cwd: d.cwd})
		if err != nil {
			return mods.ToolOutput{Text: err.Error(), IsError: true}, nil
		}
		return mods.ToolOutput{Text: msg.TextOf(res.Content), IsError: res.IsError}, nil
	}

	s.Commands = func() []mods.CommandInfo {
		reg := d.rt.registry.Load()
		if reg == nil {
			return nil
		}
		var out []mods.CommandInfo
		for _, c := range reg.Raw() {
			out = append(out, mods.CommandInfo{Name: slashcommands.QualifiedName(c), Description: c.Description,
				Source: commandSource(c.Origin), Plugin: c.Namespace})
		}
		return out
	}

	// RunCommand runs a slash command and returns what it printed. A command
	// that opens a panel, hands over the terminal, quits or expands to a
	// prompt for the model does only its printed part here. A mod's own
	// command is answered by its command.run hook, which has already run.
	s.RunCommand = func(ctx context.Context, name, args string) (mods.CommandOutput, error) {
		reg := d.rt.registry.Load()
		if reg == nil {
			return mods.CommandOutput{}, fmt.Errorf("kiln has no command registry yet")
		}
		if e := d.rt.get(); e != nil {
			for _, c := range e.Commands() {
				if c.Name == name {
					return mods.CommandOutput{}, nil
				}
			}
		}
		if _, ok := reg.Get(name); !ok {
			return mods.CommandOutput{}, fmt.Errorf("no command named %q", name)
		}
		line := "/" + name
		if args != "" {
			line += " " + args
		}
		res, err := reg.Execute(ctx, line)
		if err != nil {
			return mods.CommandOutput{}, err
		}
		if res == nil {
			return mods.CommandOutput{}, nil
		}
		return mods.CommandOutput{Text: strings.Join(res.Output, "\n")}, nil
	}

	s.RegisterAgent = func(_ context.Context, spec map[string]any) error {
		def := agents.Definition{Source: agents.Plugin}
		def.Name, _ = spec["name"].(string)
		def.Description, _ = spec["description"].(string)
		def.Prompt, _ = spec["prompt"].(string)
		def.Model, _ = spec["model"].(string)
		if list, ok := spec["tools"].([]any); ok {
			for _, v := range list {
				if n, ok := v.(string); ok {
					def.Tools = append(def.Tools, n)
				}
			}
		}
		return d.dispatcher.AddAgent(def)
	}

	// SpawnAgent starts the subagent on its own goroutine and returns at once:
	// its answer is its turn.complete. The context is marked as having raised
	// agent.spawn, so the dispatcher's own hook does not raise it again.
	s.SpawnAgent = func(ctx context.Context, in map[string]any) (mods.AgentStarted, error) {
		typ, _ := in["subagentType"].(string)
		found := false
		for _, def := range d.dispatcher.AgentList() {
			if def.Name == typ {
				found = true
				break
			}
		}
		if !found {
			return mods.AgentStarted{}, fmt.Errorf("no agent named %q", typ)
		}
		id := fmt.Sprintf("mod-agent-%d", modAgentSeq.Add(1))
		req := agent.DispatchRequest{Agent: typ, ToolCallID: id}
		req.Prompt, _ = in["prompt"].(string)
		req.Description, _ = in["description"].(string)
		req.Model, _ = in["model"].(string)
		model := req.Model
		if model == "" {
			model = d.rt.model
		}
		crash.Go(func() {
			dctx, cancel := context.WithTimeout(ctx, time.Hour)
			defer cancel()
			if _, err := d.dispatcher.Dispatch(dctx, req); err != nil {
				diag.L().Warn("mods: $.agent.spawn run failed", "agent", typ, "err", err)
			}
		})
		return mods.AgentStarted{Model: model, AgentID: id}, nil
	}
}
