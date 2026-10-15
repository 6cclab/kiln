package mods

import (
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dop251/goja"
	"github.com/google/uuid"

	"github.com/andrepato/harness/internal/crash"
)

//go:embed prelude.js
var preludeSource string

// errBudget is what the watchdog interrupts the runtime with when one
// synchronous run of JavaScript outlasts a hook's budget (an endless loop).
var errBudget = errors.New("ran past its budget without yielding")

// worker owns the goja runtime: one goroutine runs every job, so nothing
// else ever touches the runtime, as Claude Code runs every mod on one worker
// thread. Jobs are posted from any goroutine; a job may post more.
type worker struct {
	vm *goja.Runtime

	mu     sync.Mutex
	queue  []func()
	wake   chan struct{}
	closed bool
	done   chan struct{}

	// busyMu guards busySince and the interrupt flag, so the watchdog only
	// interrupts a job that is still running.
	busyMu    sync.Mutex
	busySince time.Time
	budget    time.Duration
	stopWatch chan struct{}

	onPanic func(r any, stack []byte)
}

func newWorker(budget time.Duration, onPanic func(any, []byte)) *worker {
	w := &worker{
		vm:        goja.New(),
		wake:      make(chan struct{}, 1),
		done:      make(chan struct{}),
		budget:    budget,
		stopWatch: make(chan struct{}),
		onPanic:   onPanic,
	}
	crash.Go(w.run)
	crash.Go(w.watch)
	return w
}

// post queues f to run on the worker; false once the worker is closed.
func (w *worker) post(f func()) bool {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return false
	}
	w.queue = append(w.queue, f)
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
	return true
}

// do runs f on the worker and waits for it; false when the worker closed
// first or never ran it.
func (w *worker) do(f func()) bool {
	ran := make(chan struct{})
	if !w.post(func() { f(); close(ran) }) {
		return false
	}
	select {
	case <-ran:
		return true
	case <-w.done:
		select {
		case <-ran:
			return true
		default:
			return false
		}
	}
}

func (w *worker) close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
	<-w.done
	close(w.stopWatch)
}

func (w *worker) run() {
	defer close(w.done)
	for {
		w.mu.Lock()
		jobs := w.queue
		w.queue = nil
		closed := w.closed
		w.mu.Unlock()
		if closed {
			return
		}
		if len(jobs) == 0 {
			<-w.wake
			continue
		}
		for _, job := range jobs {
			w.exec(job)
		}
	}
}

// exec runs one job under the watchdog. A Go panic inside a job (a bug in a
// native binding, or in goja) is reported and the worker carries on: a mod
// must not take kiln down, and the engine holds no state a half-run job
// could leave torn beyond the one dispatch it belonged to.
func (w *worker) exec(job func()) {
	w.busyMu.Lock()
	w.busySince = time.Now()
	w.busyMu.Unlock()
	defer func() {
		w.busyMu.Lock()
		w.busySince = time.Time{}
		w.vm.ClearInterrupt()
		w.busyMu.Unlock()
		if r := recover(); r != nil && w.onPanic != nil {
			w.onPanic(r, debug.Stack())
		}
	}()
	job()
}

// watch interrupts JavaScript that has run without yielding for longer than
// a hook's budget: the only way an endless loop in a mod gives the worker back.
func (w *worker) watch() {
	tick := w.budget / 4
	if tick < 10*time.Millisecond {
		tick = 10 * time.Millisecond
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-w.stopWatch:
			return
		case <-t.C:
			w.busyMu.Lock()
			if !w.busySince.IsZero() && time.Since(w.busySince) > w.budget {
				w.vm.Interrupt(errBudget)
			}
			w.busyMu.Unlock()
		}
	}
}

// natives are the Go halves of the prelude's web APIs.
func (w *worker) natives(log func(level, text string)) map[string]any {
	vm := w.vm
	started := time.Now()
	bytesOf := func(buf goja.Value, off, n int64) []byte {
		ab, ok := buf.Export().(goja.ArrayBuffer)
		if !ok {
			panic(vm.NewTypeError("expected an ArrayBuffer"))
		}
		b := ab.Bytes()
		if off < 0 || n < 0 || off+n > int64(len(b)) {
			panic(vm.NewTypeError("view out of range"))
		}
		return b[off : off+n]
	}
	return map[string]any{
		"btoa": func(s string) string {
			b := make([]byte, 0, len(s))
			for _, r := range s {
				if r > 0xff {
					panic(vm.NewGoError(fmt.Errorf("InvalidCharacterError: btoa takes Latin-1 text, got U+%04X", r)))
				}
				b = append(b, byte(r))
			}
			return base64.StdEncoding.EncodeToString(b)
		},
		"atob": func(s string) string {
			b, err := decodeBase64(s)
			if err != nil {
				panic(vm.NewGoError(fmt.Errorf("InvalidCharacterError: atob: %v", err)))
			}
			r := make([]rune, len(b))
			for i, c := range b {
				r[i] = rune(c)
			}
			return string(r)
		},
		"utf8Encode": func(s string) goja.ArrayBuffer {
			return vm.NewArrayBuffer([]byte(s))
		},
		"utf8Decode": func(buf goja.Value, off, n int64) string {
			b := bytesOf(buf, off, n)
			if utf8.Valid(b) {
				return string(b)
			}
			return string([]rune(string(b)))
		},
		"base64Encode": func(buf goja.Value, off, n int64) string {
			return base64.StdEncoding.EncodeToString(bytesOf(buf, off, n))
		},
		"base64Decode": func(s string) goja.ArrayBuffer {
			b, err := decodeBase64(s)
			if err != nil {
				panic(vm.NewGoError(fmt.Errorf("fromBase64: %v", err)))
			}
			return vm.NewArrayBuffer(b)
		},
		"log":         log,
		"monotonicMs": func() float64 { return float64(time.Since(started).Microseconds()) / 1000 },
		"randomUUID":  func() string { return uuid.NewString() },
	}
}

// decodeBase64 takes padded or unpadded base64, ignoring ASCII whitespace,
// as atob does.
func decodeBase64(s string) ([]byte, error) {
	clean := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case ' ', '\t', '\n', '\f', '\r':
		default:
			clean = append(clean, c)
		}
	}
	for len(clean)%4 != 0 {
		clean = append(clean, '=')
	}
	return base64.StdEncoding.DecodeString(string(clean))
}
