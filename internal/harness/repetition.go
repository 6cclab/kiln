package harness

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/andrepato/harness/internal/msg"
)

// A reply that degenerates into the same text over and over (a weak or
// quantized model at low temperature) streams until the output limit,
// burning minutes on a local GPU; the stall watch never fires because
// tokens keep coming. repeatWatch catches it as it streams. Hermes-agent
// guards against the same loop (NousResearch, MIT).
const (
	repeatMinPeriod   = 20   // shortest repeated chunk considered, in bytes
	repeatMaxPeriod   = 1000 // longest
	repeatMinSpan     = 1500 // bytes of repetition before it counts
	repeatMinReps     = 4    // copies of the chunk before it counts
	repeatMinDistinct = 6    // distinct non-space runes a chunk needs
	repeatCheckEvery  = 128  // bytes of new text between checks
	repeatWindow      = 8192 // trailing bytes of a block kept for checking

	// maxRepetitionRecoveries is how many times in one operation a
	// repeating reply is cut and the model asked to go on before the turn
	// ends with a fault.
	maxRepetitionRecoveries = 2
	repetitionSource        = "Repetition"
)

// RepetitionError is a reply kiln cut because it started repeating
// itself. Keep is how many bytes of block ContentIndex to keep: the text
// up to the end of the chunk's first copy.
type RepetitionError struct {
	ContentIndex int
	Keep         int
	Chunk        string
}

func (e *RepetitionError) Error() string {
	return fmt.Sprintf("the reply kept repeating %q", clipRunes(strings.TrimSpace(e.Chunk), 80))
}

// repeatWatch follows the text and thinking deltas of one request and
// cancels it with a *RepetitionError once a block's tail turns periodic.
type repeatWatch struct {
	cancel  context.CancelCauseFunc
	blocks  map[int]*repeatBlock
	tripped bool
}

type repeatBlock struct {
	tail      []byte // the block's trailing bytes, at most repeatWindow
	total     int    // bytes the block has streamed
	unchecked int    // bytes since the last check
}

func watchRepetition(cancel context.CancelCauseFunc) *repeatWatch {
	return &repeatWatch{cancel: cancel, blocks: map[int]*repeatBlock{}}
}

func (w *repeatWatch) saw(ev msg.StreamEvent) {
	if w.tripped || (ev.Type != msg.EventTextDelta && ev.Type != msg.EventThinkingDelta) || ev.Delta == "" {
		return
	}
	b := w.blocks[ev.ContentIndex]
	if b == nil {
		b = &repeatBlock{}
		w.blocks[ev.ContentIndex] = b
	}
	b.tail = append(b.tail, ev.Delta...)
	if over := len(b.tail) - repeatWindow; over > 0 {
		b.tail = append(b.tail[:0], b.tail[over:]...)
	}
	b.total += len(ev.Delta)
	b.unchecked += len(ev.Delta)
	if b.unchecked < repeatCheckEvery {
		return
	}
	b.unchecked = 0
	if span, period, ok := periodicTail(b.tail); ok {
		w.tripped = true
		// Keep everything before the repeating span plus one copy.
		keep := b.total - span + period
		w.cancel(&RepetitionError{ContentIndex: ev.ContentIndex, Keep: keep, Chunk: string(b.tail[len(b.tail)-period:])})
	}
}

// periodicTail finds the shortest period p for which the end of tail
// repeats itself for at least repeatMinSpan bytes and repeatMinReps
// copies, with a chunk varied enough not to be a rule or a run of one
// character. It returns the length of the periodic span and p.
func periodicTail(tail []byte) (span, period int, ok bool) {
	n := len(tail)
	for p := repeatMinPeriod; p <= repeatMaxPeriod && p*repeatMinReps <= n; p++ {
		need := max(repeatMinSpan, p*repeatMinReps)
		if need > n {
			break
		}
		// The last need bytes are p-periodic iff they equal themselves
		// shifted by p.
		if string(tail[n-need+p:]) != string(tail[n-need:n-p]) {
			continue
		}
		if !variedChunk(tail[n-p:]) {
			continue
		}
		// Extend the span back as far as it stays periodic.
		s := need
		for s < n && tail[n-s-1] == tail[n-s-1+p] {
			s++
		}
		return s, p, true
	}
	return 0, 0, false
}

func variedChunk(chunk []byte) bool {
	seen := map[rune]bool{}
	for _, r := range string(chunk) {
		if !unicode.IsSpace(r) {
			seen[r] = true
		}
	}
	return len(seen) >= repeatMinDistinct
}

// trimRepetition is the reply partial cut where the repetition started:
// block rep.ContentIndex keeps rep.Keep bytes (backed off to a rune
// boundary) and every later block is dropped, unfinished tool calls with
// them. It is a finished reply as far as the transcript is concerned.
func trimRepetition(partial *msg.AssistantMessage, rep *RepetitionError) *msg.AssistantMessage {
	out := *partial
	out.StopReason = msg.StopStop
	out.ErrorMessage = ""
	out.Content = nil
	for i, c := range partial.Content {
		if i > rep.ContentIndex {
			break
		}
		if i == rep.ContentIndex {
			switch v := c.(type) {
			case msg.TextContent:
				v.Text = cutAt(v.Text, rep.Keep)
				c = v
			case msg.ThinkingContent:
				v.Thinking = cutAt(v.Thinking, rep.Keep)
				v.ThinkingSignature = "" // the signature covered the whole block
				c = v
			}
		}
		if _, isCall := c.(msg.ToolCall); isCall {
			continue
		}
		out.Content = append(out.Content, c)
	}
	return &out
}

func cutAt(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// repetitionContinuation asks the model to go on after its reply was cut
// for repeating chunk.
func repetitionContinuation(rep *RepetitionError) string {
	return fmt.Sprintf("Your last response started repeating the same text over and over (%q), so kiln cut it there. "+
		"Do not repeat it. Continue from where the useful part ended: take the next step, or finish your answer.", clipRunes(strings.TrimSpace(rep.Chunk), 120))
}

// repetitionExhausted is the fault when replies kept repeating after
// every continuation. Its message is a sentence for the user.
type repetitionExhausted struct{ rep *RepetitionError }

func (e repetitionExhausted) Error() string {
	return fmt.Sprintf("The model kept repeating itself (%q) after %d nudges, so kiln stopped the turn. "+
		"Try again, rephrase the request, or use a different model; a higher temperature or repetition penalty on the server also helps.",
		clipRunes(strings.TrimSpace(e.rep.Chunk), 80), maxRepetitionRecoveries)
}
