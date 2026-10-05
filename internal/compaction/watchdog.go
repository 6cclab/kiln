package compaction

import (
	"sync"
	"time"
)

// Watchdog runs a callback once a stream has gone quiet for too long. Each
// Reset re-arms it with a new limit, and only the latest arming can fire.
// Both stall watchdogs use it: this package's summarization requests
// (runSimpleWatched) and the harness's model requests (harness/stall.go).
//
// Stopping the previous timer is not enough on its own. time.Timer.Stop
// does not wait for an AfterFunc callback that has already started, so a
// limit that expires just as a token arrives could still cancel the
// request after the token re-armed the watchdog. Each arming therefore
// carries a generation, and its callback fires only if, holding the lock,
// it is still the current generation and the watchdog has not been
// stopped. Fire runs under that lock, so a Reset either happens first
// (and the stale callback does nothing) or after (and the request was
// genuinely silent for the whole limit). Fire must not call back into the
// Watchdog.
type Watchdog struct {
	mu    sync.Mutex
	timer stopper
	gen   uint64
	done  bool
}

// stopper is the part of *time.Timer the Watchdog uses.
type stopper interface{ Stop() bool }

// afterFunc arms a timer; a test replaces it to fire callbacks by hand.
var afterFunc = func(d time.Duration, f func()) stopper { return time.AfterFunc(d, f) }

// NewWatchdog returns a Watchdog that calls fire unless Reset or Stop
// comes within d.
func NewWatchdog(d time.Duration, fire func()) *Watchdog {
	w := &Watchdog{}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.arm(d, fire)
	return w
}

// Reset re-arms the watchdog with a new limit and callback, superseding
// the current one. It does nothing once the watchdog has fired or stopped.
func (w *Watchdog) Reset(d time.Duration, fire func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done {
		return
	}
	w.timer.Stop()
	w.arm(d, fire)
}

// Stop disarms the watchdog for good. No callback fires after it returns.
func (w *Watchdog) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.done = true
	w.timer.Stop()
}

// arm starts the timer for a new generation. w.mu must be held.
func (w *Watchdog) arm(d time.Duration, fire func()) {
	w.gen++
	gen := w.gen
	w.timer = afterFunc(d, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.done || w.gen != gen {
			return
		}
		w.done = true
		fire()
	})
}
