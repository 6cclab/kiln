package tui

import (
	"fmt"
	"math"
	"time"
)

// The "error" block, live variant (docs/kiln-design-handoff/README.md block
// table, "error" row): a red message line plus a dim countdown row while a
// retryable stream failure is being retried. Replaced by a committed
// `RenderError` block on final failure, or preceded by a committed
// "↺ Reconnected on attempt N" note (note.go) once the retry succeeds.

// RetryView is the live retry block's state, held on Model.retry while a
// retry is pending. Attempt is the 1-based attempt that just failed
// (harness/events.go's EventRetryScheduled.Attempt, harness/turn.go's own
// 1-based attempt counter); RenderRetry displays Attempt+1, which is
// exactly the next (about to run) attempt's 1-based number. Until is the
// wall-clock time the countdown reaches zero.
type RetryView struct {
	Message string
	Attempt int
	Max     int
	Until   time.Time
}

// RenderRetry renders the live "error" block: a red `error ───` label rule,
// the humanised failure message in red, and a dim countdown row
// ("Retrying in Ns · attempt A of M · r to retry now"), where A is the
// attempt about to run (Attempt+1, 1-based for display) and N is the whole
// seconds left until Until, rounded up and clamped at zero so the row never
// shows a negative countdown in the frame just before the retry fires.
func RenderRetry(v RetryView, now time.Time, width int) []string {
	secs := int(math.Ceil(v.Until.Sub(now).Seconds()))
	if secs < 0 {
		secs = 0
	}
	return []string{
		labelRule("error", KilnRed, "", width),
		FitStatus(KilnRed(v.Message), width),
		FitStatus(Muted(fmt.Sprintf("Retrying in %ds · attempt %d of %d · r to retry now", secs, v.Attempt+1, v.Max)), width),
	}
}
