package mods

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestToolListAndCallRunThroughHooks(t *testing.T) {
	var gotName, gotID string
	var gotArgs map[string]any
	h := newHarness(t, Options{Services: Services{
		Tools: func() []ToolInfo { return []ToolInfo{{Name: "Read", Description: "reads", MCP: false}} },
		CallTool: func(_ context.Context, tool string, args json.RawMessage, id string) (ToolOutput, error) {
			gotName, gotID = tool, id
			_ = json.Unmarshal(args, &gotArgs)
			if tool == "Bash" {
				return ToolOutput{Deny: "denied by the gate"}, nil
			}
			return ToolOutput{Text: "file text"}, nil
		},
	}})
	h.load(t, "recorder")
	h.e.SessionStart(ctxT(t))

	rows, _ := call(t, h, "tool.list")["ok"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["name"] != "Read" {
		t.Errorf("tool.list = %v", rows)
	}

	out := call(t, h, "tool.call", map[string]any{"tool": "Read", "file_path": "a.go", "consent": "words"})
	res, _ := out["ok"].(map[string]any)
	if res["text"] != "file text" || gotName != "Read" || gotID == "" {
		t.Errorf("tool.call = %v (name %q id %q)\n%s", out, gotName, gotID, h.logs)
	}
	if !reflect.DeepEqual(gotArgs, map[string]any{"file_path": "a.go"}) {
		t.Errorf("the tool got %v, want only its own arguments", gotArgs)
	}
	if n := len(seenOf(t, h, "tool.call")); n != 1 {
		t.Errorf("the tool.call hooks saw %d calls, want 1", n)
	}
	out = call(t, h, "tool.call", map[string]any{"tool": "Bash", "command": "ls"})
	if res, _ := out["ok"].(map[string]any); res["deny"] != "denied by the gate" {
		t.Errorf("a refused call = %v, want {deny}", out)
	}
}

func TestToolCheckQueryAndRealCall(t *testing.T) {
	h := newHarness(t, Options{Services: Services{
		CheckTool: func(_ context.Context, tool string, _ json.RawMessage) (ToolVerdict, error) {
			return ToolVerdict{Decision: "ask"}, nil
		},
	}})
	h.load(t, "recorder", "describer")
	ctx := ctxT(t)
	h.e.SessionStart(ctx)

	out := call(t, h, "tool.check", map[string]any{"tool": "Read", "input": map[string]any{"file_path": "a"}})
	if res, _ := out["ok"].(map[string]any); res["decision"] != "ask" {
		t.Errorf("tool.check = %v, want core's ask", out)
	}
	// A real call: the describer denies an rm -rf, the gate's answer stands otherwise.
	d, why, ok := h.e.ToolCheck(ctx, "Bash", json.RawMessage(`{"command":"rm -rf /"}`), "t1", "")
	if !ok || d != "deny" || why != "no rm -rf here" {
		t.Errorf("ToolCheck(rm -rf) = %q %q %v", d, why, ok)
	}
	d, _, ok = h.e.ToolCheck(ctx, "Bash", json.RawMessage(`{"command":"ls"}`), "t2", "")
	if !ok || d != "ask" {
		t.Errorf("ToolCheck(ls) = %q %v, want core's ask", d, ok)
	}
	if n := len(seenOf(t, h, "tool.check")); n != 3 {
		t.Errorf("the recorder saw %d tool.check events, want 3 (query + two real calls)", n)
	}
}

func TestDescribeEventsRewritePresentation(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "describer")
	ctx := ctxT(t)
	h.e.SessionStart(ctx)
	if got := h.e.ToolDescribe(ctx, "Read", "reads files", false); got != "reads files [described]" {
		t.Errorf("ToolDescribe = %q", got)
	}
	desc, hint := h.e.CommandDescribe(ctx, "help", "shows help", "")
	if desc != "D: shows help" || hint != "<hint>" {
		t.Errorf("CommandDescribe = %q %q", desc, hint)
	}
	if h.e.AgentOffer(ctx, "secret", "d", "plugin") {
		t.Error("agent.offer {isOffered:false} still offered the agent")
	}
	if !h.e.AgentOffer(ctx, "Explore", "d", "built-in") {
		t.Error("an agent no hook matched was not offered")
	}
}

func TestCommandListRunAndAgentCalls(t *testing.T) {
	var ran, regName string
	var spawned map[string]any
	h := newHarness(t, Options{Services: Services{
		Commands: func() []CommandInfo {
			return []CommandInfo{{Name: "help", Description: "h", Source: "builtin"}, {Name: "x", Description: "d", Source: "plugin", Plugin: "p"}}
		},
		RunCommand: func(_ context.Context, c, a string) (CommandOutput, error) {
			ran = c + "|" + a
			return CommandOutput{Text: "printed"}, nil
		},
		RegisterAgent: func(_ context.Context, spec map[string]any) error {
			regName, _ = spec["name"].(string)
			return nil
		},
		SpawnAgent: func(_ context.Context, in map[string]any) (AgentStarted, error) {
			spawned = in
			return AgentStarted{Model: "m1", AgentID: "a-1"}, nil
		},
	}})
	h.load(t, "recorder")
	h.e.SessionStart(ctxT(t))

	rows, _ := call(t, h, "command.list")["ok"].([]any)
	if len(rows) != 2 || rows[1].(map[string]any)["plugin"] != "p" {
		t.Errorf("command.list = %v", rows)
	}
	out := call(t, h, "command.run", map[string]any{"command": "help", "args": "me"})
	if res, _ := out["ok"].(map[string]any); res["text"] != "printed" || ran != "help|me" {
		t.Errorf("command.run = %v ran %q", out, ran)
	}
	out = call(t, h, "agent.register", map[string]any{"name": "scout", "description": "d", "prompt": "p"})
	if res, _ := out["ok"].(map[string]any); res["agent"] != "scout" || regName != "scout" {
		t.Errorf("agent.register = %v (service saw %q)", out, regName)
	}
	out = call(t, h, "agent.spawn", map[string]any{"prompt": "do it", "description": "task"})
	res, _ := out["ok"].(map[string]any)
	if res["agentId"] != "a-1" || res["model"] != "m1" || spawned["subagentType"] != "general-purpose" || spawned["prompt"] != "do it" {
		t.Errorf("agent.spawn = %v, service saw %v\n%s", out, spawned, h.logs)
	}
	if n := len(seenOf(t, h, "agent.spawn")); n != 1 {
		t.Errorf("agent.spawn hooks saw %d events, want 1", n)
	}
	if n := len(seenOf(t, h, "agent.register")); n != 1 {
		t.Errorf("agent.register hooks saw %d events, want 1", n)
	}
}
