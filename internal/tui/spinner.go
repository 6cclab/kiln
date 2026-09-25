package tui

import "time"

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
// truncated (not wrapped) row while busy. The spinner is one row by
// definition, and a two-row spinner makes the whole transcript above it
// jump on each tick.
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
	line := RenderSpinner(SpinnerArgs{
		Frame:          s.frame,
		Label:          s.label,
		ElapsedSeconds: elapsedSeconds,
		Thinking:       s.thinking,
		Effort:         s.effort,
		Tokens:         tokens,
	})
	return []string{FitStatus(line, width)}
}
