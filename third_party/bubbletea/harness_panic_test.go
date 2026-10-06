package tea

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// panicRenderer panics the first time the renderer goroutine flushes.
type panicRenderer struct {
	nilRenderer
	flushes atomic.Int32
}

func (r *panicRenderer) flush(closing bool) error {
	if !closing && r.flushes.Add(1) == 1 {
		panic("renderer broke")
	}
	return nil
}

// kiln patch: a panic while the renderer goroutine paints a frame is
// recovered like a Cmd's: Run returns ErrProgramPanic (the terminal is
// restored on the way out) instead of the process dying in raw mode, and
// the shutdown that follows does not hang on the dead goroutine's stop
// channel. The panic hook sees it.
func TestHarnessRendererPanicIsRecovered(t *testing.T) {
	var buf bytes.Buffer
	var in bytes.Buffer
	var hooked sync.Map
	p := NewProgram(&testModel{},
		WithInput(&in),
		WithOutput(&buf),
		WithPanicHook(func(r any, stack []byte) { hooked.Store(r, string(stack)) }),
	)
	p.renderer = &panicRenderer{}

	done := make(chan error, 1)
	go func() {
		_, err := p.Run()
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrProgramPanic) {
			t.Fatalf("Run = %v, want ErrProgramPanic", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the renderer panicked")
	}
	stack, ok := hooked.Load("renderer broke")
	if !ok {
		t.Fatal("the panic hook never saw the renderer's panic")
	}
	if !strings.Contains(stack.(string), "panicRenderer") {
		t.Errorf("hook stack lacks the panic site:\n%s", stack)
	}
}

// The panic hook also sees a panic in Update.
func TestHarnessPanicHookSeesUpdatePanic(t *testing.T) {
	var buf bytes.Buffer
	var in bytes.Buffer
	m := &testModel{}
	var hooked atomic.Value
	p := NewProgram(m,
		WithInput(&in),
		WithOutput(&buf),
		WithPanicHook(func(r any, stack []byte) { hooked.Store(r) }),
	)
	go func() {
		for m.executed.Load() == nil {
			time.Sleep(time.Millisecond)
		}
		p.Send(panicMsg{})
	}()
	if _, err := p.Run(); !errors.Is(err, ErrProgramPanic) {
		t.Fatalf("Run = %v, want ErrProgramPanic", err)
	}
	if hooked.Load() != "testing panic behavior" {
		t.Errorf("hook saw %v", hooked.Load())
	}
}
