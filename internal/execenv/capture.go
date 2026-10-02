package execenv

import (
	"strings"
)

// SanitizeShellOutput removes bytes and sequences that are unsafe to
// render as shell output or to hand back to the model: this was
// originally a single-byte C0 filter mirroring pi's
// sanitizeShellOutput/INVALID_SHELL_OUTPUT pattern, and is now
// StripControlSequences (ansi.go), which does the same C0/interlinear-
// annotation stripping but also removes whole ANSI/terminal control
// sequences (SGR colour codes, OSC/DCS strings, CSI cursor moves) instead
// of leaving their bracket-code text behind once only the leading ESC
// byte is gone. Kept as its own name because this is the shell-output
// call site; internal/harness/toolout.go calls StripControlSequences
// directly for every other tool's result text, so both paths share one
// implementation.
func SanitizeShellOutput(text string) string {
	return StripControlSequences(text)
}

// Retention selects which end of bounded output survives truncation.
type Retention string

const (
	RetainTail Retention = "tail"
	RetainHead Retention = "head"
)

// CaptureLimits are the source-side limits for one bounded shell output
// view, mirroring pi's ShellOutputLimits.
type CaptureLimits struct {
	MaxBytes int
	MaxLines int
	// Retain defaults to RetainTail.
	Retain Retention
}

// ShellOutputMetadata accompanies a bounded shell output view, mirroring
// pi's ShellOutputMetadata.
type ShellOutputMetadata struct {
	Truncation    TruncationResult
	SpillPath     string
	LastLineBytes int
	HasLastLine   bool
}

// ShellOutputView is a complete bounded shell output view, mirroring pi's
// ShellOutputView.
type ShellOutputView struct {
	Text string
	ShellOutputMetadata
}

// UpdateKind discriminates ShellOutputUpdate. These are the four kinds pi
// emits from OutputCapture: a full replace, an append of new tail text, a
// slide that drops bytes off the front of the retained buffer before
// appending, or a metadata-only change with no text delta.
type UpdateKind string

const (
	UpdateReplace  UpdateKind = "replace"
	UpdateAppend   UpdateKind = "append"
	UpdateSlide    UpdateKind = "slide"
	UpdateMetadata UpdateKind = "metadata"
)

// ShellOutputUpdate is one incremental change to a bounded shell output
// view, mirroring pi's ShellOutputUpdate union.
//
//   - Replace carries the full text under Output.
//   - Append carries only the new tail text in Text.
//   - Slide drops Drop bytes off the front of the previously retained
//     text before appending Text.
//   - Metadata carries no text delta; only Metadata changed (for example a
//     spill path appearing).
type ShellOutputUpdate struct {
	Kind     UpdateKind
	Output   ShellOutputView // Kind == UpdateReplace
	Text     string          // Kind == UpdateAppend or UpdateSlide
	Drop     int             // Kind == UpdateSlide
	Metadata ShellOutputMetadata
}

// ApplyShellOutputUpdate folds an update onto the current view, mirroring
// pi's applyShellOutputUpdate. current may be nil for the first update.
func ApplyShellOutputUpdate(current *ShellOutputView, update ShellOutputUpdate) ShellOutputView {
	switch update.Kind {
	case UpdateReplace:
		return update.Output
	case UpdateAppend:
		text := ""
		if current != nil {
			text = current.Text
		}
		return ShellOutputView{Text: text + update.Text, ShellOutputMetadata: update.Metadata}
	case UpdateSlide:
		text := ""
		if current != nil {
			text = current.Text[update.Drop:]
		}
		return ShellOutputView{Text: text + update.Text, ShellOutputMetadata: update.Metadata}
	case UpdateMetadata:
		text := ""
		if current != nil {
			text = current.Text
		}
		return ShellOutputView{Text: text, ShellOutputMetadata: update.Metadata}
	default:
		if current != nil {
			return *current
		}
		return ShellOutputView{}
	}
}

// guardMultiplier bounds how large the retained raw buffer is allowed to
// grow before it is trimmed to 2*maxBytes, mirroring nodejs.js's guard
// (maxBytes * 2, trimmed once bufferBytes exceeds guard * 2).
const guardMultiplier = 2

// OutputCapture maintains and publishes one bounded shell-output view,
// mirroring pi's OutputCapture (utils/output-capture.js) minus the
// adaptive rate-limited publication: every Push calls onUpdate
// synchronously with the update it computes, which is simpler and
// sufficient for a subprocess's lifetime in this port.
type OutputCapture struct {
	maxBytes int
	maxLines int
	retain   Retention
	onUpdate func(ShellOutputUpdate)

	buffer           string
	bufferBytes      int
	totalBytes       int
	newlines         int
	endsWithNewline  bool
	currentLineBytes int
	spillPath        string

	previous *ShellOutputView
}

// NewOutputCapture builds a capture with the given limits. onUpdate may be
// nil to discard updates (the final Snapshot is still available).
func NewOutputCapture(limits CaptureLimits, onUpdate func(ShellOutputUpdate)) *OutputCapture {
	maxBytes := limits.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	maxLines := limits.MaxLines
	if maxLines <= 0 {
		maxLines = DefaultMaxLines
	}
	retain := limits.Retain
	if retain == "" {
		retain = RetainTail
	}
	return &OutputCapture{
		maxBytes:        maxBytes,
		maxLines:        maxLines,
		retain:          retain,
		onUpdate:        onUpdate,
		endsWithNewline: true,
	}
}

// Truncated reports whether the total output seen so far exceeds either
// limit.
func (c *OutputCapture) Truncated() bool {
	return c.totalBytes > c.maxBytes || c.totalLines() > c.maxLines
}

func (c *OutputCapture) totalLines() int {
	if c.endsWithNewline || c.totalBytes == 0 {
		return c.newlines
	}
	return c.newlines + 1
}

// Push appends a chunk of output and publishes an update if onUpdate is
// set.
func (c *OutputCapture) Push(chunk string) {
	if chunk == "" {
		return
	}
	textBytes := len(chunk)
	c.totalBytes += textBytes
	c.newlines += strings.Count(chunk, "\n")
	c.endsWithNewline = strings.HasSuffix(chunk, "\n")
	if lastNewline := strings.LastIndex(chunk, "\n"); lastNewline == -1 {
		c.currentLineBytes += textBytes
	} else {
		c.currentLineBytes = len(chunk) - lastNewline - 1
	}
	c.buffer += chunk
	c.bufferBytes += textBytes

	guard := c.maxBytes * guardMultiplier
	if c.bufferBytes > guard*2 {
		if c.retain == RetainTail {
			c.buffer = trimToLastBytes(c.buffer, guard)
		} else {
			c.buffer = trimToFirstBytes(c.buffer, guard)
		}
		c.bufferBytes = len(c.buffer)
	}

	c.publish()
}

// SetSpillPath records where the complete output is being preserved once
// truncation has begun, and publishes the metadata change.
func (c *OutputCapture) SetSpillPath(path string) {
	if c.spillPath == path {
		return
	}
	c.spillPath = path
	c.publish()
}

// Snapshot returns the current bounded view.
func (c *OutputCapture) Snapshot() ShellOutputView {
	var retained TruncationResult
	opts := TruncateOptions{MaxLines: c.maxLines, MaxBytes: c.maxBytes}
	if c.retain == RetainHead {
		retained = TruncateHead(c.buffer, opts)
	} else {
		retained = TruncateTail(c.buffer, opts)
	}
	totalLines := c.totalLines()
	truncated := c.Truncated()
	truncatedBy := TruncatedByNone
	if truncated {
		if totalLines > c.maxLines {
			truncatedBy = TruncatedByLines
		} else {
			truncatedBy = TruncatedByBytes
		}
	}
	truncation := retained
	truncation.Content = ""
	truncation.Truncated = truncated
	truncation.TruncatedBy = truncatedBy
	truncation.TotalBytes = c.totalBytes
	truncation.TotalLines = totalLines

	meta := ShellOutputMetadata{Truncation: truncation, SpillPath: c.spillPath}
	if retained.LastLinePartial {
		meta.HasLastLine = true
		meta.LastLineBytes = c.currentLineBytes
	}
	return ShellOutputView{Text: SanitizeShellOutput(retained.Content), ShellOutputMetadata: meta}
}

func (c *OutputCapture) publish() {
	if c.onUpdate == nil {
		return
	}
	current := c.Snapshot()
	update := updateFrom(c.previous, current)
	c.previous = &current
	c.onUpdate(update)
}

// updateFrom computes the incremental update from previous to current,
// mirroring pi's updateFrom: an append when current is previous plus a
// tail, a slide when the two share a shorter overlapping suffix/prefix, a
// metadata-only update when the text is unchanged, and a full replace
// otherwise.
func updateFrom(previous *ShellOutputView, current ShellOutputView) ShellOutputUpdate {
	if previous == nil {
		return ShellOutputUpdate{Kind: UpdateReplace, Output: current}
	}
	metadata := current.ShellOutputMetadata
	if current.Text == previous.Text {
		return ShellOutputUpdate{Kind: UpdateMetadata, Metadata: metadata}
	}
	if len(current.Text) > len(previous.Text) && current.Text[:len(previous.Text)] == previous.Text {
		return ShellOutputUpdate{Kind: UpdateAppend, Text: current.Text[len(previous.Text):], Metadata: metadata}
	}
	scan := min3(len(previous.Text), len(current.Text), current.Truncation.MaxBytes*2)
	if shared := suffixPrefixOverlap(previous.Text, current.Text, scan); shared > 0 {
		return ShellOutputUpdate{
			Kind:     UpdateSlide,
			Drop:     len(previous.Text) - shared,
			Text:     current.Text[shared:],
			Metadata: metadata,
		}
	}
	return ShellOutputUpdate{Kind: UpdateReplace, Output: current}
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// suffixPrefixOverlap finds the longest suffix of before (within the last
// scan bytes) that is a prefix of after, mirroring pi's
// suffixPrefixOverlap: it probes with the first 64 bytes of after, then
// falls back to a single-byte probe, each capped at 8 candidate positions.
func suffixPrefixOverlap(before, after string, scan int) int {
	if len(before) == 0 || len(after) == 0 || scan == 0 {
		return 0
	}
	tail := before
	if len(before) > scan {
		tail = before[len(before)-scan:]
	}
	probeLengths := []int{min2(64, len(after)), 1}
	for _, probeLength := range probeLengths {
		probe := after[:probeLength]
		candidates := 0
		for index := strings.Index(tail, probe); index != -1; {
			candidates++
			if candidates > 8 {
				break
			}
			overlapLength := len(tail) - index
			if overlapLength <= len(after) && tail[index:] == after[:overlapLength] {
				return overlapLength
			}
			next := strings.Index(tail[index+1:], probe)
			if next == -1 {
				index = -1
			} else {
				index = index + 1 + next
			}
		}
		if probeLength == 1 {
			break
		}
	}
	return 0
}

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func trimToLastBytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	start := len(s) - maxBytes
	for start < len(s) && isUTF8Continuation(s[start]) {
		start++
	}
	return s[start:]
}

func trimToFirstBytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	end := maxBytes
	for end > 0 && isUTF8Continuation(s[end]) {
		end--
	}
	return s[:end]
}
