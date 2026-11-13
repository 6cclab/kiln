package mods

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dop251/goja"

	"github.com/andrepato/harness/internal/crash"
)

// Services are what kiln lends the $ methods that reach beyond the engine:
// the conversation, compaction, the running turn. Each is optional; a $ call
// whose service is nil rejects, naming itself. Calls run on a background
// goroutine, so a service may block. The context carries the calling $
// method's marks (MarkRaised), which a service passes down to kiln.
type Services struct {
	// Messages lists a conversation as claude-code.d.ts's SessionMessage
	// ({ role, text, toolUses, toolResults? }); agentID "" is the main one.
	// api asks for ApiMessage ({ role, content }) instead.
	Messages func(ctx context.Context, agentID string, api bool) ([]any, error)
	// Compact compacts the main conversation now with the instructions, as a
	// plugin asked, and returns the conversation it left.
	Compact func(ctx context.Context, instructions string) (CompactResult, error)
	// Append queues a user-role text row on a conversation ("" the main one)
	// and returns its uuid.
	Append func(ctx context.Context, agentID, text string) (string, error)
	// AbortTurn cancels the main loop's running turn turnID; it errors, naming
	// both ids, when turnID is not the running turn's.
	AbortTurn func(ctx context.Context, turnID string) error

	// Tools lists the tools the model has, as the model names them.
	Tools func() []ToolInfo
	// CallTool runs a tool through kiln's permission gate (which may prompt
	// the person) and returns its output; a refusal is ToolOutput.Deny.
	CallTool func(ctx context.Context, tool string, args json.RawMessage, toolUseID string) (ToolOutput, error)
	// CheckTool is what the permission gate would decide for a call, asking
	// nobody.
	CheckTool func(ctx context.Context, tool string, input json.RawMessage) (ToolVerdict, error)
	// Commands lists the slash commands; RunCommand runs one (the text it
	// printed) without raising command.run again for a mod's own command.
	Commands   func() []CommandInfo
	RunCommand func(ctx context.Context, command, args string) (CommandOutput, error)
	// RegisterAgent makes an agent type (AgentSpec's keys) dispatchable.
	RegisterAgent func(ctx context.Context, spec map[string]any) error
	// SpawnAgent starts a subagent in the background (AgentSpawnInput's keys,
	// as the hooks left them) and returns once it started. The context is
	// marked as having raised agent.spawn.
	SpawnAgent func(ctx context.Context, in map[string]any) (AgentStarted, error)
}

// CompactResult is what a compaction left: the conversation after it and the
// tokens before and after (0 when unknown).
type CompactResult struct {
	Messages     []any
	TokensBefore int
	TokensAfter  int
}

// goAsync runs fn on a background goroutine and returns a promise settled on
// the worker with its value (nil settles undefined) or error.
func (e *Engine) goAsync(fn func() (any, error)) goja.Value {
	vm := e.w.vm
	promise, resolve, reject := vm.NewPromise()
	go func() {
		defer crash.Guard()
		out, err := fn()
		e.w.post(func() {
			if err != nil {
				_ = reject(e.errorValue(err.Error()))
				return
			}
			if out == nil {
				_ = resolve(goja.Undefined())
				return
			}
			v, jerr := e.toJS(out)
			if jerr != nil {
				_ = reject(e.errorValue(jerr.Error()))
				return
			}
			_ = resolve(v)
		})
	}()
	return vm.ToValue(promise)
}

// opCall serves a $ method that raises an event: prepare builds the event on
// a background goroutine (it may block), the chain runs every hook but the
// calling one, core is kiln's part (built with a context marked as raised),
// and shape turns the chain's result into the promise's value.
func (e *Engine) opCall(f *frame, event string, prepare func(ctx context.Context) (map[string]any, error),
	core func(ctx context.Context) OpCore, shape func(res map[string]any) (any, error)) goja.Value {
	hk := f.h
	return e.goAsync(func() (any, error) {
		ctx := withoutHook(MarkRaised(context.Background(), event), hk)
		ev, err := prepare(ctx)
		if err != nil {
			return nil, err
		}
		var oc OpCore
		if core != nil {
			oc = core(ctx)
		}
		res, err := e.Raise(ctx, event, ev, oc)
		if err != nil {
			return nil, err
		}
		if shape == nil {
			return res, nil
		}
		return shape(res)
	})
}

func exportMap(v goja.Value) map[string]any {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	m, _ := v.Export().(map[string]any)
	return m
}

func strOf(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func noService(name string) error {
	return fmt.Errorf("%s is not available: kiln has no such service wired", name)
}

// answerOps serves the $ methods that raise events or call Services; handled
// is false for a name it does not know.
func (e *Engine) answerOps(f *frame, name string, arg func(int) goja.Value) (out any, handled bool, err error) {
	svc := e.opts.Services
	switch name {
	case "session.compact":
		if svc.Compact == nil || svc.Messages == nil {
			return nil, true, noService(name)
		}
		args := exportMap(arg(0))
		instructions := strOf(args, "instructions")
		return e.opCall(f, "session.compact",
			func(ctx context.Context) (map[string]any, error) {
				msgs, err := svc.Messages(ctx, "", false)
				if err != nil {
					return nil, err
				}
				ev := map[string]any{"trigger": "plugin", "messages": msgs}
				if instructions != "" {
					ev["instructions"] = instructions
				}
				return ev, nil
			},
			func(ctx context.Context) OpCore {
				return func(ev map[string]any, done func(map[string]any, error)) {
					res, err := svc.Compact(ctx, strOf(ev, "instructions"))
					if err != nil {
						done(nil, err)
						return
					}
					out := map[string]any{"messages": res.Messages}
					if res.TokensBefore > 0 {
						out["tokensBefore"] = res.TokensBefore
					}
					if res.TokensAfter > 0 {
						out["tokensAfter"] = res.TokensAfter
					}
					done(out, nil)
				}
			}, func(res map[string]any) (any, error) {
				if s := strOf(res, "skip"); s != "" {
					return map[string]any{"skip": s}, nil
				}
				return res, nil
			}), true, nil

	case "session.append":
		if svc.Append == nil {
			return nil, true, noService(name)
		}
		args := exportMap(arg(0))
		message, _ := args["message"].(map[string]any)
		agentID := strOf(args, "agentId")
		deny := ""
		var text string
		switch {
		case message == nil:
			return nil, true, fmt.Errorf("takes { message: { type, content }, agentId? }")
		case strOf(message, "type") != "user":
			deny = "kiln keeps no " + strOf(message, "type") + " rows in a conversation: only a user-role text row"
		default:
			blocks, _ := message["content"].([]any)
			if s, ok := message["content"].(string); ok {
				blocks = []any{map[string]any{"type": "text", "text": s}}
			}
			for _, b := range blocks {
				bm, _ := b.(map[string]any)
				if strOf(bm, "type") != "text" {
					deny = "this release appends text blocks alone, not " + strOf(bm, "type")
					break
				}
				if text != "" {
					text += "\n"
				}
				text += strOf(bm, "text")
			}
		}
		if deny != "" {
			return e.promiseOf(e.mustJS(map[string]any{"deny": deny})), true, nil
		}
		return e.goAsync(func() (any, error) {
			uuid, err := svc.Append(withoutHook(context.Background(), f.h), agentID, text)
			if err != nil {
				return map[string]any{"deny": err.Error()}, nil
			}
			return map[string]any{"message": message, "uuid": uuid}, nil
		}), true, nil

	case "turn.abort":
		if svc.AbortTurn == nil {
			return nil, true, noService(name)
		}
		args := exportMap(arg(0))
		turnID := strOf(args, "turnId")
		if turnID == "" {
			return nil, true, fmt.Errorf("takes { turnId }")
		}
		return e.opCall(f, "turn.abort",
			func(context.Context) (map[string]any, error) { return map[string]any{"turnId": turnID}, nil },
			func(ctx context.Context) OpCore {
				return func(ev map[string]any, done func(map[string]any, error)) {
					done(map[string]any{}, svc.AbortTurn(ctx, strOf(ev, "turnId")))
				}
			}, func(map[string]any) (any, error) { return nil, nil }), true, nil
	}
	return e.answerTools(f, name, arg)
}
