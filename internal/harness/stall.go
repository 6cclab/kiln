package harness

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/andrepato/harness/internal/compaction"
	"github.com/andrepato/harness/internal/msg"
)

// StallError is a model request kiln gave up on because the model sent
// nothing for too long: no first token within the allowance for the
// prompt's size, or no further token for the idle limit once it started.
//
// A request has no total deadline. A slow local model may take many
// minutes to read a large prompt and then stream for many more; only
// silence ends it. The limits are compaction's (internal/compaction/fit.go:
// DefaultFirstEventTimeout, DefaultIdleTimeout), so an ordinary turn and a
// compaction on the same slow host get the same patience.
type StallError struct {
	Waited       time.Duration
	FirstToken   bool // waiting for the first token, not the next one
	PromptTokens int
}

func (e *StallError) Error() string {
	what := "the next token"
	if e.FirstToken {
		what = "the first token"
	}
	return fmt.Sprintf("the model sent nothing for %s while waiting for %s (prompt ~%s tokens)", roundWait(e.Waited), what, kTokens(e.PromptTokens))
}

// Timeout marks a stall as a timeout, so isRetriable's net.Error-style
// checks and callers that test for one recognise it.
func (e *StallError) Timeout() bool { return true }

// stallLimits returns the first-token allowance for a prompt of
// promptTokens and the idle limit, from Options or compaction's defaults.
func (o Options) stallLimits(promptTokens int) (first, idle time.Duration) {
	first = compaction.DefaultFirstEventTimeout(promptTokens)
	if o.StallFirstEvent != nil {
		first = o.StallFirstEvent(promptTokens)
	}
	idle = compaction.DefaultIdleTimeout
	if o.StallIdle > 0 {
		idle = o.StallIdle
	}
	return first, idle
}

// stallWatch cancels a request's context with a *StallError when the
// stream goes quiet. The provider's start event does not count as
// progress: it follows the response headers, which some servers send
// before they have read the prompt, so only content moves the request
// from the first-token allowance to the idle limit.
type stallWatch struct {
	mu     sync.Mutex
	timer  *time.Timer
	cancel context.CancelCauseFunc
	idle   time.Duration
	prompt int
}

func watchStall(cancel context.CancelCauseFunc, first, idle time.Duration, promptTokens int) *stallWatch {
	w := &stallWatch{cancel: cancel, idle: idle, prompt: promptTokens}
	w.timer = time.AfterFunc(first, func() {
		cancel(&StallError{Waited: first, FirstToken: true, PromptTokens: promptTokens})
	})
	return w
}

// saw records a stream event, restarting the idle limit.
func (w *stallWatch) saw(ev msg.StreamEvent) {
	if ev.Type == msg.EventStart {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.timer.Stop()
	idle, prompt, cancel := w.idle, w.prompt, w.cancel
	w.timer = time.AfterFunc(idle, func() {
		cancel(&StallError{Waited: idle, PromptTokens: prompt})
	})
}

func (w *stallWatch) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.timer.Stop()
}

func roundWait(d time.Duration) time.Duration {
	if d < time.Second {
		return d.Round(time.Millisecond)
	}
	return d.Round(time.Second)
}
