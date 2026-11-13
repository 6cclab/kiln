package mods

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"

	"github.com/dop251/goja"

	"github.com/andrepato/harness/internal/crash"
)

// $.process.spawn and $.mcp.connect.
//
// A mod's child process never runs ungated: spawn asks Services.GateProcess,
// which kiln answers with the permission gate the Bash tool goes through (the
// argument vector as the command it would be), and with no gate wired the
// stream rejects its first pull without starting anything. mcp.connect starts
// nothing: kiln connects a plugin's MCP servers at session start, so the call
// reports what that did (Services.McpConnect).

// McpConnectAnswer is what $.mcp.connect resolves: the server connected under
// Server, or why not (Reason is Claude Code's one-word spelling, Message the
// sentence).
type McpConnectAnswer struct {
	Connected       bool
	Server          string
	Reason, Message string
}

// spawnChunkCap is how many characters may wait unread before the child
// blocks on its next write (claude-code.d.ts, ProcessSpawnChunk).
const spawnChunkCap = 1 << 20

// procSet is the children in flight, so closing the engine ends them.
type procSet struct {
	mu     sync.Mutex
	all    map[*spawned]struct{}
	closed bool
}

func (ps *procSet) add(s *spawned) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.closed {
		return false
	}
	if ps.all == nil {
		ps.all = map[*spawned]struct{}{}
	}
	ps.all[s] = struct{}{}
	return true
}

func (ps *procSet) remove(s *spawned) {
	ps.mu.Lock()
	delete(ps.all, s)
	ps.mu.Unlock()
}

func (ps *procSet) killAll() {
	ps.mu.Lock()
	ps.closed = true
	all := make([]*spawned, 0, len(ps.all))
	for s := range ps.all {
		all = append(all, s)
	}
	ps.mu.Unlock()
	for _, s := range all {
		s.kill()
	}
}

type spawnPiece struct{ stream, text string }

type spawnWaiter struct {
	resolve func(any) error
	reject  func(any) error
}

// spawned is one $.process.spawn child and the stream that reads it.
type spawned struct {
	e      *Engine
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	cond     *sync.Cond
	cmd      *exec.Cmd
	q        []spawnPiece
	queued   int
	done     bool
	result   map[string]any
	err      error // why it did not start; reported by the first pull
	errSent  bool
	returned bool // the loop left (return()): the child is killed, the stream over
	killed   bool
	waiters  []spawnWaiter
}

func (s *spawned) kill() {
	s.mu.Lock()
	s.killed = true
	cmd := s.cmd
	s.cond.Broadcast()
	s.mu.Unlock()
	s.cancel()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// fail ends the stream before any child ran.
func (s *spawned) fail(err error) {
	s.mu.Lock()
	s.err, s.done = err, true
	s.mu.Unlock()
	s.e.w.post(s.settle)
}

// push queues one piece, waiting while a megacharacter is unread.
func (s *spawned) push(stream, text string) {
	s.mu.Lock()
	for s.queued >= spawnChunkCap && !s.killed {
		s.cond.Wait()
	}
	if !s.killed {
		s.q = append(s.q, spawnPiece{stream, text})
		s.queued += utf8.RuneCountInString(text)
	}
	s.mu.Unlock()
	s.e.w.post(s.settle)
}

// settle answers the pending pulls from what is queued; worker-only.
func (s *spawned) settle() {
	vm := s.e.w.vm
	type action struct {
		w   spawnWaiter
		val goja.Value
		err error
	}
	var acts []action
	s.mu.Lock()
	for len(s.waiters) > 0 {
		w := s.waiters[0]
		switch {
		case s.returned:
			s.waiters = s.waiters[1:]
			acts = append(acts, action{w: w, val: s.iterResult(true, goja.Undefined())})
		case len(s.q) > 0:
			s.waiters = s.waiters[1:]
			p := s.q[0]
			s.q = s.q[1:]
			s.queued -= utf8.RuneCountInString(p.text)
			s.cond.Broadcast()
			chunk := vm.NewObject()
			_ = chunk.Set("stream", p.stream)
			_ = chunk.Set("text", p.text)
			acts = append(acts, action{w: w, val: s.iterResult(false, chunk)})
		case s.done && s.err != nil && !s.errSent:
			s.waiters = s.waiters[1:]
			s.errSent = true
			acts = append(acts, action{w: w, err: s.err})
		case s.done:
			s.waiters = s.waiters[1:]
			val := goja.Undefined()
			if s.err == nil && s.result != nil {
				if v, err := s.e.toJS(s.result); err == nil {
					val = v
				}
			}
			acts = append(acts, action{w: w, val: s.iterResult(true, val)})
		default:
			goto out
		}
	}
out:
	s.mu.Unlock()
	for _, a := range acts {
		if a.err != nil {
			_ = a.w.reject(s.e.errorValue(a.err.Error()))
		} else {
			_ = a.w.resolve(a.val)
		}
	}
}

func (s *spawned) iterResult(done bool, value goja.Value) goja.Value {
	o := s.e.w.vm.NewObject()
	_ = o.Set("done", done)
	_ = o.Set("value", value)
	return o
}

// run gates, starts and reads the child; goroutine.
func (s *spawned) run(gate func(context.Context) (string, error), argv []string, dir string, env map[string]string, input string) {
	defer crash.Guard()
	defer s.cancel()
	deny, err := gate(s.ctx)
	if err != nil {
		s.fail(fmt.Errorf("process.spawn: %w", err))
		return
	}
	if deny != "" {
		s.fail(fmt.Errorf("process.spawn was refused: %s", deny))
		return
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.fail(err)
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		s.fail(err)
		return
	}
	s.mu.Lock()
	if s.killed {
		s.mu.Unlock()
		s.fail(errors.New("process.spawn: the call ended before the child started"))
		return
	}
	if err := cmd.Start(); err != nil {
		s.mu.Unlock()
		s.fail(err)
		return
	}
	s.cmd = cmd
	s.mu.Unlock()
	if !s.e.procs.add(s) {
		s.kill()
	}
	defer s.e.procs.remove(s)

	var wg sync.WaitGroup
	for name, r := range map[string]io.Reader{"stdout": stdout, "stderr": stderr} {
		wg.Add(1)
		go func(name string, r io.Reader) {
			defer wg.Done()
			defer crash.Guard()
			s.read(name, r)
		}(name, r)
	}
	wg.Wait()
	_ = cmd.Wait()
	res := map[string]any{"code": nil, "signal": nil}
	if ps := cmd.ProcessState; ps != nil {
		if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res["signal"] = signalName(ws.Signal())
		} else {
			res["code"] = ps.ExitCode()
		}
	}
	s.mu.Lock()
	s.result, s.done = res, true
	s.mu.Unlock()
	s.cancel()
	s.e.w.post(s.settle)
}

// read pushes what r yields as UTF-8 text, never splitting a character.
func (s *spawned) read(stream string, r io.Reader) {
	buf := make([]byte, 32<<10)
	var carry []byte
	for {
		n, err := r.Read(buf)
		if n > 0 {
			b := append(carry, buf[:n]...)
			carry = nil
			for i := 1; i <= utf8.UTFMax && i <= len(b); i++ {
				if utf8.RuneStart(b[len(b)-i]) {
					if !utf8.FullRune(b[len(b)-i:]) {
						carry = append([]byte(nil), b[len(b)-i:]...)
						b = b[:len(b)-i]
					}
					break
				}
			}
			if len(b) > 0 {
				s.push(stream, string(b))
			}
		}
		if err != nil {
			if len(carry) > 0 {
				s.push(stream, string(carry))
			}
			return
		}
	}
}

func signalName(sig syscall.Signal) string {
	names := map[syscall.Signal]string{
		syscall.SIGHUP: "SIGHUP", syscall.SIGINT: "SIGINT", syscall.SIGQUIT: "SIGQUIT",
		syscall.SIGABRT: "SIGABRT", syscall.SIGKILL: "SIGKILL", syscall.SIGSEGV: "SIGSEGV",
		syscall.SIGPIPE: "SIGPIPE", syscall.SIGALRM: "SIGALRM", syscall.SIGTERM: "SIGTERM",
	}
	if n, ok := names[sig]; ok {
		return n
	}
	return fmt.Sprintf("SIG%d", int(sig))
}

// spawnProcess serves $.process.spawn: it returns the stream at once (an
// async iterator of { stream, text } pieces that ends with { code, signal });
// the child starts on a goroutine, after the gate agrees. Leaving the loop
// (return()) or closing the engine kills it. Worker-only.
func (e *Engine) spawnProcess(f *frame, req goja.Value) goja.Value {
	vm := e.w.vm
	ctx, cancel := context.WithCancel(context.Background())
	s := &spawned{e: e, ctx: ctx, cancel: cancel}
	s.cond = sync.NewCond(&s.mu)

	in := exportMap(req)
	var argv []string
	if a, ok := in["argv"].([]any); ok {
		for _, v := range a {
			argv = append(argv, fmt.Sprint(v))
		}
	}
	dir := e.opts.Cwd
	if c := strOf(in, "cwd"); c != "" {
		dir = e.fsPath(c)
	}
	env := map[string]string{}
	if m, ok := in["env"].(map[string]any); ok {
		for k, v := range m {
			env[k] = fmt.Sprint(v)
		}
	}
	gateFn := e.opts.Services.GateProcess
	switch {
	case len(argv) == 0:
		s.fail(errors.New("process.spawn({ argv }): argv must be a non-empty array"))
	case gateFn == nil:
		s.fail(errors.New("process.spawn is not available: kiln has no permission gate wired for it, and never runs a mod's process ungated"))
	default:
		plugin := f.plugin.name
		go s.run(func(ctx context.Context) (string, error) {
			return gateFn(ctx, plugin, argv, dir, env)
		}, argv, dir, env, strOf(in, "input"))
	}

	it := vm.NewObject()
	_ = it.Set("next", func(goja.FunctionCall) goja.Value {
		p, resolve, reject := vm.NewPromise()
		s.mu.Lock()
		s.waiters = append(s.waiters, spawnWaiter{resolve, reject})
		s.mu.Unlock()
		s.settle()
		return vm.ToValue(p)
	})
	_ = it.Set("return", func(call goja.FunctionCall) goja.Value {
		s.mu.Lock()
		s.returned = true
		s.q, s.queued = nil, 0
		s.mu.Unlock()
		s.kill()
		s.settle()
		p, resolve, _ := vm.NewPromise()
		_ = resolve(s.iterResult(true, call.Argument(0)))
		return vm.ToValue(p)
	})
	if sym, ok := vm.Get("Symbol").ToObject(vm).Get("asyncIterator").(*goja.Symbol); ok {
		_ = it.SetSymbol(sym, func(goja.FunctionCall) goja.Value { return it })
	}
	return it
}

// mcpConnect serves $.mcp.connect: what kiln's connection of the calling
// plugin's own MCP server came to.
func (e *Engine) mcpConnect(f *frame, arg func(int) goja.Value) (any, error) {
	svc := e.opts.Services
	server := str(arg(0))
	if server == "" {
		return nil, fmt.Errorf("takes the server's key in this plugin's manifest")
	}
	if svc.McpConnect == nil {
		return nil, noService("mcp.connect")
	}
	plugin := f.plugin.name
	return e.goAsync(func() (any, error) {
		a := svc.McpConnect(context.Background(), plugin, server)
		if a.Connected {
			return map[string]any{"isConnected": true, "server": a.Server}, nil
		}
		return map[string]any{"isConnected": false, "reason": a.Reason, "message": a.Message}, nil
	}), nil
}
