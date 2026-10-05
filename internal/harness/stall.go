package harness

import (
	"context"
	"fmt"
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
//
// The timing is compaction.Watchdog's, shared with summarization requests,
// so a limit that expires just as a token arrives cannot cancel the
// request that token resumed.
type stallWatch struct {
	dog    *compaction.Watchdog
	idle   time.Duration
	onIdle func()
}

func watchStall(cancel context.CancelCauseFunc, first, idle time.Duration, promptTokens int) *stallWatch {
	return &stallWatch{
		dog: compaction.NewWatchdog(first, func() {
			cancel(&StallError{Waited: first, FirstToken: true, PromptTokens: promptTokens})
		}),
		idle: idle,
		onIdle: func() {
			cancel(&StallError{Waited: idle, PromptTokens: promptTokens})
		},
	}
}

// saw records a stream event, restarting the idle limit.
func (w *stallWatch) saw(ev msg.StreamEvent) {
	if ev.Type == msg.EventStart {
		return
	}
	w.dog.Reset(w.idle, w.onIdle)
}

func (w *stallWatch) stop() { w.dog.Stop() }

func roundWait(d time.Duration) time.Duration {
	if d < time.Second {
		return d.Round(time.Millisecond)
	}
	return d.Round(time.Second)
}
