package mods

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHost records what the engine asked of kiln.
type fakeHost struct {
	mu            sync.Mutex
	panes         []string
	opened        []PaneRequest
	toasts        []string
	invalidations int
	agents        []AgentInfo
	usage         Usage
	settings      json.RawMessage
}

func (h *fakeHost) OpenPane(plugin string, req PaneRequest) PaneResult {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.opened = append(h.opened, req)
	for _, p := range h.panes {
		if p == req.ID {
			return PaneResult{IsPlaced: true}
		}
	}
	h.panes = append(h.panes, req.ID)
	return PaneResult{IsPlaced: true}
}

func (h *fakeHost) ClosePane(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, p := range h.panes {
		if p == id {
			h.panes = append(h.panes[:i], h.panes[i+1:]...)
			return true
		}
	}
	return false
}

func (h *fakeHost) Panes() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.panes...)
}

func (h *fakeHost) Toast(plugin, text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.toasts = append(h.toasts, plugin+": "+text)
}

func (h *fakeHost) Invalidate() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.invalidations++
}

func (h *fakeHost) Agents() []AgentInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]AgentInfo(nil), h.agents...)
}

func (h *fakeHost) SessionUsage() Usage { return h.usage }

func (h *fakeHost) Settings() json.RawMessage { return h.settings }

func (h *fakeHost) invalidated() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.invalidations
}

// fakeClock fires timers only when advanced.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	f       func()
	stopped bool
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !t.stopped
		t.stopped = true
		return was
	}
}

// pending is how many timers are waiting.
func (c *fakeClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if !t.stopped {
			n++
		}
	}
	return n
}

// advance moves time forward in steps, firing what falls due and letting the
// engine run the fired timers (and reschedule) after each step.
func (c *fakeClock) advance(e *Engine, d, step time.Duration) {
	for moved := time.Duration(0); moved < d; moved += step {
		c.mu.Lock()
		c.now = c.now.Add(step)
		var due []*fakeTimer
		var keep []*fakeTimer
		for _, t := range c.timers {
			switch {
			case t.stopped:
			case !t.at.After(c.now):
				t.stopped = true
				due = append(due, t)
			default:
				keep = append(keep, t)
			}
		}
		c.timers = keep
		c.mu.Unlock()
		sort.Slice(due, func(i, j int) bool { return due[i].at.Before(due[j].at) })
		for _, t := range due {
			t.f()
		}
		e.sync()
		e.sync()
	}
}

// logBuffer collects the engine's log lines.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type harness struct {
	e     *Engine
	host  *fakeHost
	clock *fakeClock
	logs  *logBuffer
}

func newHarness(t *testing.T, opts Options) *harness {
	t.Helper()
	h := &harness{host: &fakeHost{}, clock: newFakeClock(), logs: &logBuffer{}}
	if opts.Clock == nil {
		opts.Clock = h.clock
	}
	opts.Logger = slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if opts.LookupEnv == nil {
		opts.LookupEnv = func(string) (string, bool) { return "", false }
	}
	h.e = New(h.host, opts)
	t.Cleanup(h.e.Close)
	return h
}

// load loads fixture mods from testdata/mods/<name>.
func (h *harness) load(t *testing.T, names ...string) {
	t.Helper()
	var mods []Module
	for _, n := range names {
		mods = append(mods, fixture(t, n))
	}
	if errs := h.e.Load(mods); len(errs) > 0 {
		t.Fatalf("load: %v\nlogs:\n%s", errs, h.logs)
	}
}

func fixture(t *testing.T, name string) Module {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("testdata", "mods", name))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []string{"register.ts", "register.tsx"} {
		p := filepath.Join(root, "hooks", entry)
		if _, err := os.Stat(p); err == nil {
			return Module{Plugin: name, Root: root, Path: p}
		}
	}
	t.Fatalf("fixture %s has no hooks/register.ts[x]", name)
	return Module{}
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// texts returns every text in a tree, in order.
func texts(el *Element) []string {
	if el == nil {
		return nil
	}
	var out []string
	var walk func(Element)
	walk = func(n Element) {
		if n.Type == "" && n.Text != "" {
			out = append(out, n.Text)
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(*el)
	return out
}

func joined(el *Element) string { return strings.Join(texts(el), "") }

// find returns the first element of a type whose text contains want.
func find(el *Element, typ, want string) *Element {
	if el == nil {
		return nil
	}
	var hit *Element
	var walk func(n *Element)
	walk = func(n *Element) {
		if hit != nil {
			return
		}
		if n.Type == typ && strings.Contains(joined(n), want) {
			hit = n
			return
		}
		for i := range n.Children {
			walk(&n.Children[i])
		}
	}
	walk(el)
	return hit
}
