package mods

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/dop251/goja"
)

// Tool, command and agent services and events: $.tool.*, $.command.*,
// $.agent.register/spawn, and the events that describe or gate them.

// coreOrigin is the provider of everything kiln itself supplies.
func coreOrigin() map[string]any { return map[string]any{"plugin": "engine", "tier": "core"} }

// ToolInfo is one tool as $.tool.list() returns it.
type ToolInfo struct {
	Name, Description string
	MCP               bool
}

// ToolOutput is what running a tool produced: its text, or Deny when kiln's
// permission gate refused the call.
type ToolOutput struct {
	Text    string
	IsError bool
	Deny    string
}

// ToolVerdict is the permission decision for a call: allow, ask or deny.
type ToolVerdict struct {
	Decision string
	Reason   string
}

// CommandInfo is one slash command as $.command.list() returns it. Source is
// claude-code.d.ts's CommandSource spelling.
type CommandInfo struct {
	Name, Description, Source, Plugin string
}

// CommandOutput is what a command printed.
type CommandOutput struct {
	Text    string
	Context []string
}

// AgentStarted is a subagent a $.agent.spawn call started.
type AgentStarted struct {
	Model   string
	AgentID string
}

var modToolSeq atomic.Int64

func (e *Engine) answerTools(f *frame, name string, arg func(int) goja.Value) (out any, handled bool, err error) {
	svc := e.opts.Services
	switch name {
	case "tool.list":
		if svc.Tools == nil {
			return nil, true, noService(name)
		}
		return e.goAsync(func() (any, error) {
			rows := []any{}
			for _, t := range svc.Tools() {
				rows = append(rows, map[string]any{"name": t.Name, "description": t.Description, "mcp": t.MCP})
			}
			return rows, nil
		}), true, nil

	case "tool.call":
		if svc.CallTool == nil {
			return nil, true, noService(name)
		}
		in := exportMap(arg(0))
		if strOf(in, "tool") == "" {
			return nil, true, fmt.Errorf("takes { tool, tool_use_id?, ...arguments }")
		}
		return e.opCall(f, "tool.call",
			func(context.Context) (map[string]any, error) {
				ev := map[string]any{}
				for k, v := range in {
					if k != "consent" {
						ev[k] = v
					}
				}
				if strOf(ev, "tool_use_id") == "" {
					ev["tool_use_id"] = fmt.Sprintf("toolu_mod_%d", modToolSeq.Add(1))
				}
				return ev, nil
			},
			func(ctx context.Context) OpCore {
				return func(ev map[string]any, done func(map[string]any, error)) {
					args := map[string]any{}
					for k, v := range ev {
						switch k {
						case "tool", "tool_use_id", "consent", "agentId", "requestMeta":
						default:
							args[k] = v
						}
					}
					raw, _ := json.Marshal(args)
					res, err := svc.CallTool(ctx, strOf(ev, "tool"), raw, strOf(ev, "tool_use_id"))
					if err != nil {
						done(nil, err)
						return
					}
					if res.Deny != "" {
						done(map[string]any{"deny": res.Deny}, nil)
						return
					}
					out := map[string]any{"result": res.Text, "text": res.Text}
					if res.IsError {
						out["isError"] = true
					}
					done(out, nil)
				}
			}, nil), true, nil

	case "tool.check":
		if svc.CheckTool == nil {
			return nil, true, noService(name)
		}
		in := exportMap(arg(0))
		tool := strOf(in, "tool")
		if tool == "" {
			return nil, true, fmt.Errorf("takes { tool, input }")
		}
		return e.opCall(f, "tool.check",
			func(context.Context) (map[string]any, error) {
				return map[string]any{"tool": tool, "input": in["input"]}, nil
			},
			func(ctx context.Context) OpCore { return e.toolCheckCore(ctx) }, nil), true, nil

	case "command.list":
		if svc.Commands == nil {
			return nil, true, noService(name)
		}
		return e.goAsync(func() (any, error) {
			rows := []any{}
			for _, c := range svc.Commands() {
				row := map[string]any{"name": c.Name, "description": c.Description, "source": c.Source}
				if c.Plugin != "" {
					row["plugin"] = c.Plugin
				}
				rows = append(rows, row)
			}
			return rows, nil
		}), true, nil

	case "command.run":
		if svc.RunCommand == nil {
			return nil, true, noService(name)
		}
		in := exportMap(arg(0))
		cmd := strOf(in, "command")
		if cmd == "" {
			return nil, true, fmt.Errorf("takes { command, args? }")
		}
		return e.opCall(f, "command.run",
			func(context.Context) (map[string]any, error) {
				return map[string]any{"command": cmd, "args": strOf(in, "args")}, nil
			},
			func(ctx context.Context) OpCore {
				return func(ev map[string]any, done func(map[string]any, error)) {
					res, err := svc.RunCommand(ctx, strOf(ev, "command"), strOf(ev, "args"))
					if err != nil {
						done(nil, err)
						return
					}
					out := map[string]any{}
					if res.Text != "" {
						out["text"] = res.Text
					}
					if len(res.Context) > 0 {
						out["context"] = res.Context
					}
					done(out, nil)
				}
			}, nil), true, nil

	case "agent.register":
		if svc.RegisterAgent == nil {
			return nil, true, noService(name)
		}
		spec := exportMap(arg(0))
		if strOf(spec, "name") == "" || strOf(spec, "description") == "" || strOf(spec, "prompt") == "" {
			return nil, true, fmt.Errorf("takes { name, description, prompt, tools?, model? }")
		}
		return e.opCall(f, "agent.register",
			func(context.Context) (map[string]any, error) { return spec, nil },
			func(ctx context.Context) OpCore {
				return func(ev map[string]any, done func(map[string]any, error)) {
					if err := svc.RegisterAgent(ctx, ev); err != nil {
						done(nil, err)
						return
					}
					done(map[string]any{"agent": strOf(ev, "name")}, nil)
				}
			}, nil), true, nil

	case "agent.spawn":
		if svc.SpawnAgent == nil {
			return nil, true, noService(name)
		}
		in := exportMap(arg(0))
		if strOf(in, "prompt") == "" {
			return nil, true, fmt.Errorf("takes { prompt, description?, subagentType?, model?, name?, cwd? }")
		}
		return e.opCall(f, "agent.spawn",
			func(context.Context) (map[string]any, error) {
				ev := map[string]any{
					"tool_use_id": "", "prompt": strOf(in, "prompt"), "description": strOf(in, "description"),
					"subagentType": strOf(in, "subagentType"), "provider": coreOrigin(),
					"parentModel": "", "background": true, "fork": false,
				}
				if ev["subagentType"] == "" {
					ev["subagentType"] = "general-purpose"
				}
				for _, k := range []string{"model", "name", "cwd"} {
					if s := strOf(in, k); s != "" {
						ev[k] = s
					}
				}
				return ev, nil
			},
			func(ctx context.Context) OpCore {
				return func(ev map[string]any, done func(map[string]any, error)) {
					res, err := svc.SpawnAgent(ctx, ev)
					if err != nil {
						done(nil, err)
						return
					}
					done(map[string]any{"model": res.Model, "agentId": res.AgentID}, nil)
				}
			}, nil), true, nil
	}
	return e.answerSession(f, name, arg)
}

// toolCheckCore is kiln's part of tool.check: what the permission gate would
// decide, asking nobody.
func (e *Engine) toolCheckCore(ctx context.Context) OpCore {
	svc := e.opts.Services
	return func(ev map[string]any, done func(map[string]any, error)) {
		if svc.CheckTool == nil {
			done(map[string]any{"decision": "ask"}, nil)
			return
		}
		raw, _ := json.Marshal(ev["input"])
		v, err := svc.CheckTool(ctx, strOf(ev, "tool"), raw)
		if err != nil {
			done(nil, err)
			return
		}
		out := map[string]any{"decision": v.Decision}
		if v.Reason != "" {
			out["reason"] = v.Reason
		}
		done(out, nil)
	}
}

// ToolCheck raises tool.check for a real call before kiln's permission gate
// runs, and returns the decision the chain settled on and why. The chain's
// last word is the verdict, as in Claude Code, but kiln applies only a
// deny: an allow or ask a hook answers does not override the gate, which
// decides next. ok is false when no hook is registered.
func (e *Engine) ToolCheck(ctx context.Context, tool string, input json.RawMessage, toolUseID, agentID string) (decision, reason string, ok bool) {
	if e == nil || !e.Has("tool.check") {
		return "", "", false
	}
	var in any
	_ = json.Unmarshal(input, &in)
	ev := map[string]any{"tool": tool, "input": in}
	if toolUseID != "" {
		ev["tool_use_id"] = toolUseID
	}
	if agentID != "" {
		ev["agentId"] = agentID
	}
	res, err := e.Raise(ctx, "tool.check", ev, e.toolCheckCore(ctx))
	if err != nil {
		e.log.Warn("mods: tool.check chain failed; the permission gate decides", "err", err)
		return "", "", false
	}
	return strOf(res, "decision"), strOf(res, "reason"), true
}

// ToolDescribe raises tool.describe for one tool and returns the description
// the model should see ("" leaves it as it was).
func (e *Engine) ToolDescribe(ctx context.Context, tool, description string, mcp bool) string {
	if e == nil || !e.Has("tool.describe") {
		return ""
	}
	ev := map[string]any{"tool": tool, "description": description, "provider": coreOrigin()}
	if mcp {
		ev["isDeferred"] = true
	}
	res, err := e.Raise(ctx, "tool.describe", ev, OpResult(func(in map[string]any) (map[string]any, error) {
		return map[string]any{"description": in["description"]}, nil
	}))
	if err != nil {
		e.log.Warn("mods: tool.describe chain failed; keeping the description", "tool", tool, "err", err)
		return ""
	}
	if s := strOf(res, "description"); s != "" && s != description {
		return s
	}
	return ""
}

// CommandDescribe raises command.describe for one command and returns the
// description and argument hint to show. isHidden is not applied: kiln's
// typeahead has no hidden commands.
func (e *Engine) CommandDescribe(ctx context.Context, command, description, hint string) (string, string) {
	if e == nil || !e.Has("command.describe") {
		return description, hint
	}
	ev := map[string]any{"command": command, "description": description, "isHidden": false,
		"immediate": false, "provider": coreOrigin()}
	if hint != "" {
		ev["argumentHint"] = hint
	}
	res, err := e.Raise(ctx, "command.describe", ev, OpResult(func(in map[string]any) (map[string]any, error) {
		return map[string]any{"description": in["description"], "argumentHint": in["argumentHint"], "isHidden": in["isHidden"]}, nil
	}))
	if err != nil {
		e.log.Warn("mods: command.describe chain failed; keeping the description", "command", command, "err", err)
		return description, hint
	}
	if s, ok := res["description"].(string); ok {
		description = s
	}
	if s, ok := res["argumentHint"].(string); ok {
		hint = s
	}
	return description, hint
}

// AgentOffer raises agent.offer for one agent type and reports whether the
// model may be offered (dispatch) it. A chain that fails offers it.
func (e *Engine) AgentOffer(ctx context.Context, agent, description, source string) bool {
	if e == nil || !e.Has("agent.offer") {
		return true
	}
	ev := map[string]any{"agent": agent, "description": description, "source": source, "provider": coreOrigin()}
	res, err := e.Raise(ctx, "agent.offer", ev, OpResult(func(map[string]any) (map[string]any, error) {
		return map[string]any{"isOffered": true}, nil
	}))
	if err != nil {
		e.log.Warn("mods: agent.offer chain failed; offering the agent", "agent", agent, "err", err)
		return true
	}
	if b, ok := res["isOffered"].(bool); ok {
		return b
	}
	return true
}
