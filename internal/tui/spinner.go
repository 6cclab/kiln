package tui

import (
	"strings"
	"time"
)

// The working indicator, rendered above the input box.
//
// Ported from app.ts's SpinnerView (330-397). Spinner and footer are
// separate types because they live on opposite sides of the editor: the
// spinner belongs to the conversation flow above it, the status line sits
// below it.
type SpinnerState struct {
	busy      bool
	frame     int
	startedAt time.Time
	tokens    int
	// label is the turn's current gerund; baseLabel is what it resets to.
	label     string
	baseLabel string
	// thinking and effort drive RenderSpinner's suffix before tokens
	// start flowing: "(Ns · thinking with <effort> effort)".
	thinking bool
	effort   string
	// queueLen is Lane.Steer's queue length (EventQueueUpdate via
	// MsgQueue), shown as the busy line's " · N queued" suffix.
	queueLen int
}

// Start begins the spinner for a new turn, picking a gerund from seed.
func (s *SpinnerState) Start(seed int) {
	s.busy = true
	s.frame = 0
	s.tokens = 0
	s.startedAt = time.Now()
	s.label = PickLabel(seed)
	s.baseLabel = s.label
	s.thinking = false
	s.effort = ""
	s.queueLen = 0
}

// StartLabel begins the spinner for work that is not a model turn (a
// background /compact), under a fixed label instead of a turn's gerund.
func (s *SpinnerState) StartLabel(label string) {
	s.Start(0)
	s.label = label
	s.baseLabel = label
}

// SetQueueLen sets the busy line's " · N queued" suffix (0 hides it).
func (s *SpinnerState) SetQueueLen(n int) {
	s.queueLen = n
}

// Stop ends the spinner.
func (s *SpinnerState) Stop() {
	s.busy = false
}

// Tick advances the animation frame.
func (s *SpinnerState) Tick() {
	s.frame++
}

// SetLabel sets a temporary label for a phase within the turn, e.g.
// reasoning.
func (s *SpinnerState) SetLabel(label string) {
	s.label = label
}

// ResetLabel returns to the gerund this turn started with.
func (s *SpinnerState) ResetLabel() {
	s.label = s.baseLabel
}

// SetTokens replaces the live token count. Totals are cumulative for the
// run, so this replaces rather than adds.
func (s *SpinnerState) SetTokens(n int) {
	s.tokens = n
}

// Tokens returns the live token count.
func (s *SpinnerState) Tokens() int {
	return s.tokens
}

// Label returns the spinner's current gerund, for PastTense at turn end.
func (s *SpinnerState) Label() string {
	return s.label
}

// SetThinking marks whether the turn is in a reasoning phase (drives the
// "thinking with <effort> effort" suffix) and the effort label to show.
func (s *SpinnerState) SetThinking(thinking bool, effort string) {
	s.thinking = thinking
	s.effort = effort
}

// Busy reports whether a turn is in flight.
func (s *SpinnerState) Busy() bool {
	return s.busy
}

// Render renders zero or exactly one line: nothing while idle, one
// truncated (not wrapped) row while busy — the busy line: spinner+label+
// elapsed/tokens on the left, "esc to stop" right-aligned at width-1
// (docs/kiln-design-handoff/README.md "Interactions"). The spinner is one
// row by definition, and a two-row spinner makes the whole transcript
// above it jump on each tick.
func (s *SpinnerState) Render(width int, now time.Time) []string {
	if !s.busy {
		return []string{}
	}
	if now.IsZero() {
		now = time.Now()
	}
	elapsedSeconds := int(now.Sub(s.startedAt).Round(time.Second) / time.Second)
	var tokens *int
	if s.tokens != 0 {
		t := s.tokens
		tokens = &t
	}
	left := RenderSpinnerLeft(SpinnerArgs{
		Frame:          s.frame,
		Label:          s.label,
		ElapsedSeconds: elapsedSeconds,
		Thinking:       s.thinking,
		Effort:         s.effort,
		Tokens:         tokens,
		QueueLen:       s.queueLen,
	})
	right := Muted("esc to stop")
	pad := width - 1 - VisibleWidth(left) - VisibleWidth(right)
	if pad < 1 {
		return []string{FitStatus(left, width)}
	}
	return []string{left + strings.Repeat(" ", pad) + right}
}
