package mods

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dop251/goja"
)

// This file is the generic way an event runs with a Go core: the chain of
// every hook on the event (minus the calling hook, for a $ call that raises
// it), then a function kiln supplies. Raise is the entry for kiln's own call
// sites; startOp is what a $ method uses from inside a hook.

// OpCore is the core of an event: it gets the event as the hooks left it and
// answers through done, from any goroutine, with the result object or an
// error that rejects the chain.
type OpCore func(ev map[string]any, done func(res map[string]any, err error))

// OpResult is a core's synchronous answer.
func OpResult(f func(ev map[string]any) (map[string]any, error)) OpCore {
	return func(ev map[string]any, done func(map[string]any, error)) {
		done(f(ev))
	}
}

type skipKey struct{}

// WithoutHook marks ctx as made inside a hook's $ call: Raise runs every hook
// but that one ("every hook but the calling one").
func withoutHook(ctx context.Context, h *hook) context.Context {
	return context.WithValue(ctx, skipKey{}, h)
}

type raisedKey struct{}

// MarkRaised marks ctx as already carried through event: a call site that
// raises event itself (a $ method that dispatches it before calling kiln)
// marks the context it passes down so kiln's own raise point does not raise
// it a second time.
func MarkRaised(ctx context.Context, event string) context.Context {
	prev, _ := ctx.Value(raisedKey{}).(map[string]bool)
	next := map[string]bool{event: true}
	for k := range prev {
		next[k] = true
	}
	return context.WithValue(ctx, raisedKey{}, next)
}

// IsRaised reports whether ctx was marked by MarkRaised for event.
func IsRaised(ctx context.Context, event string) bool {
	m, _ := ctx.Value(raisedKey{}).(map[string]bool)
	return m[event]
}

// startOp runs event through its hooks (without skip) and then core, and
// returns a promise of the chain's result. Worker-only.
func (e *Engine) startOp(ctx context.Context, event string, input goja.Value, skip *hook, core OpCore) goja.Value {
	vm := e.w.vm
	p, resolve, reject := vm.NewPromise()
	coreFn := func(in goja.Value) goja.Value {
		cp, cres, crej := vm.NewPromise()
		if core == nil {
			_ = cres(e.mustJS(map[string]any{}))
			return vm.ToValue(cp)
		}
		ev := map[string]any{}
		if raw, ok := e.fromJS(in); ok {
			_ = json.Unmarshal(raw, &ev)
		}
		var once bool
		core(ev, func(res map[string]any, err error) {
			e.w.post(func() {
				if once {
					return
				}
				once = true
				if err != nil {
					_ = crej(e.errorValue(err.Error()))
					return
				}
				if res == nil {
					res = map[string]any{}
				}
				v, jerr := e.toJS(res)
				if jerr != nil {
					_ = crej(e.errorValue(jerr.Error()))
					return
				}
				_ = cres(v)
			})
		})
		return vm.ToValue(cp)
	}
	c := e.newChain(event, coreFn)
	if skip != nil {
		hs := c.hooks[:0:0]
		for _, h := range c.hooks {
			if h != skip {
				hs = append(hs, h)
			}
		}
		c.hooks = hs
	}
	e.runChain(c, input, func(v goja.Value, err error) {
		if err != nil {
			_ = reject(e.errorValue(err.Error()))
			return
		}
		if v == nil {
			v = goja.Undefined()
		}
		_ = resolve(v)
	})
	return vm.ToValue(p)
}

func (e *Engine) mustJS(v any) goja.Value {
	out, err := e.toJS(v)
	if err != nil {
		return goja.Undefined()
	}
	return out
}

// Raise runs event with payload ev through every hook registered on it, then
// core (nil answers {}), and returns the result the chain settled on as a
// map ({} when a hook returned something that is not an object). A hook of
// the calling $ method is left out when ctx came from one. It returns an
// error when the chain rejected, the engine closed or ctx ended first.
func (e *Engine) Raise(ctx context.Context, event string, ev map[string]any, core OpCore) (map[string]any, error) {
	skip, _ := ctx.Value(skipKey{}).(*hook)
	type answer struct {
		res map[string]any
		err error
	}
	got, err := await(ctx, e, func(send func(answer)) {
		input, err := e.toJS(ev)
		if err != nil {
			send(answer{err: err})
			return
		}
		p := e.startOp(ctx, event, input, skip, core)
		e.then(p, func(v goja.Value) {
			send(answer{res: e.objectOf(v)})
		}, func(r goja.Value) {
			send(answer{err: fmt.Errorf("%s", reasonText(r))})
		})
	})
	if err != nil {
		return nil, err
	}
	return got.res, got.err
}

// raiseBounded is Raise with its own deadline, for the call sites that take
// no context (session.end, which exits must not wait on).
func (e *Engine) raiseBounded(event string, ev map[string]any, core OpCore, bound time.Duration) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), bound)
	defer cancel()
	return e.Raise(ctx, event, ev, core)
}

// objectOf is a chain result as a map; {} for anything else.
func (e *Engine) objectOf(v goja.Value) map[string]any {
	out := map[string]any{}
	if raw, ok := e.fromJS(v); ok {
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil && m != nil {
			out = m
		}
	}
	return out
}

// Has reports whether any mod hooks event, so a call site can skip building
// a payload nobody reads. It never waits on the worker: UI paths call it.
func (e *Engine) Has(event string) bool {
	if e == nil || e.closed.Load() {
		return false
	}
	ix := e.hooked.Load()
	return ix != nil && ix.events[event]
}

// RendersComponent reports whether a ui.render hook may draw component: one
// whose matcher names it, or names no component. A transcript row asks before
// it waits on a mod to draw it.
func (e *Engine) RendersComponent(component string) bool {
	if e == nil || e.closed.Load() {
		return false
	}
	ix := e.hooked.Load()
	return ix != nil && (ix.components[component] || ix.components[""])
}
