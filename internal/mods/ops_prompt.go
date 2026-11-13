package mods

import (
	"context"
	"fmt"

	"github.com/dop251/goja"

	"github.com/andrepato/harness/internal/crash"
)

// The $.prompt.* methods: the prompt box (read, fill, suggest), submitting a
// prompt, the prompt being submitted (media) and the system prompt (compose).

// background runs fn off the worker (a core runs on the engine's goroutine
// and must not wait on the TUI) and answers through done.
func background(done func(map[string]any, error), fn func() (map[string]any, error)) {
	go func() {
		defer crash.Guard()
		done(fn())
	}()
}

func (e *Engine) answerPrompt(f *frame, name string, arg func(int) goja.Value) (out any, handled bool, err error) {
	svc := e.opts.Services
	plugin := f.plugin.name
	switch name {
	case "prompt.read":
		return e.goAsync(func() (any, error) {
			box := PromptBox{}
			if svc.PromptRead != nil {
				if b, ok := svc.PromptRead(); ok {
					box = b
				}
			}
			return map[string]any{"text": box.Text, "cursor": box.Cursor}, nil
		}), true, nil

	case "prompt.media":
		args := exportMap(arg(0))
		id := strOf(args, "id")
		if id == "" {
			return nil, true, fmt.Errorf("takes { id, offset? }")
		}
		offset := 0
		if n, ok := args["offset"].(float64); ok {
			offset = int(n)
		}
		return e.promiseOf(e.mustJS(e.media(id, offset))), true, nil

	case "prompt.fill":
		args := exportMap(arg(0))
		text, hasText := args["text"].(string)
		mode := strOf(args, "mode")
		if mode == "" {
			mode = "replace"
		}
		if !hasText || (mode != "replace" && mode != "append" && mode != "insert") {
			return nil, true, fmt.Errorf("takes { text, mode?: replace | append | insert }")
		}
		return e.opCall(f, "prompt.fill",
			func(context.Context) (map[string]any, error) {
				ev := map[string]any{"text": text, "mode": mode, "origin": map[string]any{"kind": "plugin", "name": plugin}}
				if d, ok := args["decorations"]; ok {
					ev["decorations"] = d
				}
				return ev, nil
			},
			func(ctx context.Context) OpCore {
				return func(ev map[string]any, done func(map[string]any, error)) {
					background(done, func() (map[string]any, error) {
						if svc.PromptFill == nil {
							return map[string]any{"isFilled": false, "refusal": "no_composer"}, nil
						}
						a := svc.PromptFill(ctx, strOf(ev, "text"), strOf(ev, "mode"), plugin)
						out := map[string]any{"isFilled": a.Filled}
						if a.Refusal != "" {
							out["refusal"] = a.Refusal
						}
						return out, nil
					})
				}
			},
			func(res map[string]any) (any, error) {
				box := PromptBox{}
				if svc.PromptRead != nil {
					if b, ok := svc.PromptRead(); ok {
						box = b
					}
				}
				filled, _ := res["isFilled"].(bool)
				out := map[string]any{"isFilled": filled, "text": box.Text, "cursor": box.Cursor}
				if r := strOf(res, "refusal"); r != "" {
					out["refusal"] = r
				}
				return out, nil
			}), true, nil

	case "prompt.suggest":
		args := exportMap(arg(0))
		text, ok := args["text"].(string)
		if !ok {
			return nil, true, fmt.Errorf("takes { text }")
		}
		return e.opCall(f, "prompt.suggest",
			func(context.Context) (map[string]any, error) {
				return map[string]any{"text": text, "origin": map[string]any{"kind": "plugin", "name": plugin}}, nil
			},
			func(ctx context.Context) OpCore {
				return func(ev map[string]any, done func(map[string]any, error)) {
					background(done, func() (map[string]any, error) {
						shown := svc.PromptSuggest != nil && svc.PromptSuggest(ctx, strOf(ev, "text"))
						return map[string]any{"isShown": shown}, nil
					})
				}
			},
			func(res map[string]any) (any, error) {
				shown, _ := res["isShown"].(bool)
				return map[string]any{"isShown": shown}, nil
			}), true, nil

	case "prompt.submit":
		args := exportMap(arg(0))
		text, ok := args["text"].(string)
		if !ok || text == "" {
			return nil, true, fmt.Errorf("takes { text, asUser? }")
		}
		asUser, _ := args["asUser"].(bool)
		return e.opCall(f, "prompt.submit",
			func(context.Context) (map[string]any, error) {
				origin := map[string]any{"kind": "plugin", "name": plugin}
				if asUser {
					origin["asUser"] = true
				}
				return map[string]any{"text": text, "origin": origin, "wait": false}, nil
			},
			func(ctx context.Context) OpCore {
				return func(ev map[string]any, done func(map[string]any, error)) {
					background(done, func() (map[string]any, error) {
						if svc.PromptSubmit == nil {
							return map[string]any{"drop": "kiln -p runs the one prompt it was given: there is no idle session to submit into"}, nil
						}
						ctxBlocks := strList(ev["context"])
						if err := svc.PromptSubmit(ctx, strOf(ev, "text"), ctxBlocks, plugin, asUser); err != nil {
							return map[string]any{"drop": err.Error()}, nil
						}
						out := map[string]any{"text": strOf(ev, "text"), "origin": ev["origin"]}
						if len(ctxBlocks) > 0 {
							out["context"] = ctxBlocks
						}
						return out, nil
					})
				}
			}, nil), true, nil

	case "prompt.compose":
		if svc.PromptCompose == nil {
			return nil, true, noService(name)
		}
		args := exportMap(arg(0))
		var base []PromptSection
		return e.opCall(f, "prompt.compose",
			func(ctx context.Context) (map[string]any, error) {
				facts, sections := svc.PromptCompose(ctx, args)
				base = sections
				return facts, nil
			},
			func(ctx context.Context) OpCore {
				return func(_ map[string]any, done func(map[string]any, error)) {
					background(done, func() (map[string]any, error) {
						return map[string]any{"sections": sectionMaps(e.PromptSectionsOnly(ctx, base))}, nil
					})
				}
			},
			func(res map[string]any) (any, error) {
				if list, ok := sectionsOf(res["sections"]); ok {
					return map[string]any{"sections": sectionMaps(list)}, nil
				}
				return map[string]any{"sections": sectionMaps(base)}, nil
			}), true, nil
	}
	return e.answerConfig(f, name, arg)
}
