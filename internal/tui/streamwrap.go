package tui

import (
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/parser"
)

// Incremental wrapping for the live streaming block (stream.go).
//
// A streaming reply only ever grows, and the live block shows only its last
// few wrapped rows, but wrapMultiline re-wraps the whole reply: on every
// frame, and the program renders a frame for every message it receives,
// so a delta cost time in proportion to everything streamed before it
// (finding 20261006T111858Z-stream-render-cost-grows-with-reply).
// streamWrapCache keeps what earlier frames worked out and wraps only the
// bytes a delta added. Its rows are exactly wrapMultiline's: completed
// source lines go through FitLines itself, once, when their newline
// arrives; the line still streaming goes through lineWrapper, a resumable
// form of ansi.Wrap; stream_wrap_test.go holds both to the full re-wrap
// across every split point.

// streamWrapCache holds the wrapped rows of the reply streamed so far at
// one width. It is shared by every copy of a Model (a pointer field), and
// locked because View may run on a copy.
type streamWrapCache struct {
	mu sync.Mutex

	width int
	// text is the reply the cache describes; a later text that does not
	// extend it (a new message, a different reply) starts over.
	text string
	// done holds the rows of every source line before lineStart.
	done []string
	// lineStart is the offset in text of the line still streaming.
	lineStart int
	line      lineWrapper
	lineWidth lineWidthBound
}

// tailRows returns the last n rows of wrapMultiline(text, width), and the
// total row count.
func (c *streamWrapCache) tailRows(text string, width, n int) ([]string, int) {
	if width <= 0 {
		rows := wrapMultiline(text, width)
		return lastRows(rows, n), len(rows)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.width != width || len(text) < len(c.text) || text[:len(c.text)] != c.text {
		c.reset(width)
	}
	c.advance(text)

	line := text[c.lineStart:]
	if c.lineWidth.width <= width && VisibleWidth(line) <= width {
		// FitLines passes a line that fits through as it is.
		return tailOf(n, c.done, []string{line}), len(c.done) + 1
	}
	finished, extra, last := c.line.rows(line)
	return tailOf(n, c.done, finished, extra, []string{last}), len(c.done) + len(finished) + len(extra) + 1
}

// tailOf returns, in a new slice the caller may modify, the last n rows of
// parts laid end to end, copying only those.
func tailOf(n int, parts ...[]string) []string {
	if n <= 0 {
		return nil
	}
	first, skip, size := len(parts), 0, 0 // the first part used, rows skipped in it, rows taken
	for left := n; first > 0 && left > 0; {
		first--
		if len(parts[first]) >= left {
			skip = len(parts[first]) - left
			size += left
			left = 0
		} else {
			left -= len(parts[first])
			size += len(parts[first])
		}
	}
	out := make([]string, 0, size)
	for p := first; p < len(parts); p++ {
		if p == first {
			out = append(out, parts[p][skip:]...)
		} else {
			out = append(out, parts[p]...)
		}
	}
	return out
}

func lastRows(rows []string, n int) []string {
	if n <= 0 {
		return nil
	}
	if len(rows) > n {
		return rows[len(rows)-n:]
	}
	return rows
}

// release drops the cached reply once it has committed. Safe on nil.
func (c *streamWrapCache) release() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reset(0)
}

// reset empties the cache for width; the caller holds c.mu.
func (c *streamWrapCache) reset(width int) {
	c.width = width
	c.text = ""
	c.done = nil
	c.lineStart = 0
	c.line.reset(width)
	c.lineWidth = lineWidthBound{}
}

// advance takes text, which extends c.text, into the cache: each source
// line completed since the last call is wrapped whole and kept, and the
// line still streaming is fed to the incremental wrapper.
func (c *streamWrapCache) advance(text string) {
	from := len(c.text)
	c.text = text
	for {
		nl := strings.IndexByte(text[from:], '\n')
		if nl < 0 {
			break
		}
		end := from + nl
		c.done = append(c.done, FitLines([]string{text[c.lineStart:end]}, c.width, "")...)
		c.lineStart = end + 1
		from = c.lineStart
		c.line.reset(c.width)
		c.lineWidth = lineWidthBound{}
	}
	line := text[c.lineStart:]
	c.line.feed(line)
	c.lineWidth.feed(line)
}

// lineWidthBound is ansi.StringWidth computed as a line grows: width sums
// the line's grapheme clusters up to i, never counting a cluster that
// reaches the end of the text so far (more text could still join it). A
// cluster's width is never negative, so once width passes a limit, the
// whole line's does too, for good.
type lineWidthBound struct {
	pstate parser.State
	i      int
	width  int
}

func (b *lineWidthBound) feed(s string) {
	for b.i < len(s) {
		state, action := parser.Table.Transition(b.pstate, s[b.i])
		if action == parser.PrintAction || state == parser.Utf8State {
			cluster, w := ansi.FirstGraphemeCluster(s[b.i:], ansi.GraphemeWidth)
			if b.i+len(cluster) == len(s) {
				return
			}
			b.width += w
			b.i += len(cluster)
			b.pstate = parser.GroundState
			continue
		}
		b.pstate = state
		b.i++
	}
}

// lineWrapper's wrapping loop is derived from charmbracelet/x/ansi
// v0.11.8 wrap.go, used under its licence:
//
// MIT License
//
// Copyright (c) 2023 Charmbracelet, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

// lineWrapper is ansi.Wrap(line, limit, "") (charmbracelet/x/ansi v0.11.8,
// wrap.go, MIT licence) restated so it can stop and resume: the loop and
// every branch are the library's, but its buffers live in this struct, the
// output is kept as finished rows plus the row being filled, and feed stops
// before a grapheme cluster that reaches the end of the text so far, since
// the next delta could extend that cluster. rows finishes a copy of the
// state, so the cached state keeps resuming from where feed stopped.
type lineWrapper struct {
	limit int
	i     int // bytes of the line consumed

	done       []string // finished rows
	cur        []byte   // the row being filled
	word       []byte
	space      []byte
	spaceWidth int
	curWidth   int
	wordLen    int
	pstate     parser.State
}

func (w *lineWrapper) reset(limit int) {
	*w = lineWrapper{limit: max(limit, 1)}
}

func (w *lineWrapper) feed(s string) {
	w.i = w.run(s, w.i, true)
}

// rows returns the wrapped rows of s, which feed has seen: the rows feed
// finished, the rows finishing the rest of s adds, then the last row. The
// first slice is the wrapper's own; callers must not modify it.
func (w *lineWrapper) rows(s string) (finished, extra []string, last string) {
	f := lineWrapper{
		limit:      w.limit,
		cur:        append([]byte(nil), w.cur...),
		word:       append([]byte(nil), w.word...),
		space:      append([]byte(nil), w.space...),
		spaceWidth: w.spaceWidth,
		curWidth:   w.curWidth,
		wordLen:    w.wordLen,
		pstate:     w.pstate,
	}
	f.run(s, w.i, false)
	f.finish()
	return w.done, f.done, string(f.cur)
}

func (w *lineWrapper) addSpace() {
	if w.spaceWidth == 0 && len(w.space) == 0 {
		return
	}
	w.curWidth += w.spaceWidth
	w.cur = append(w.cur, w.space...)
	w.space = w.space[:0]
	w.spaceWidth = 0
}

func (w *lineWrapper) addWord() {
	if len(w.word) == 0 {
		return
	}
	w.addSpace()
	w.curWidth += w.wordLen
	w.cur = append(w.cur, w.word...)
	w.word = w.word[:0]
	w.wordLen = 0
}

func (w *lineWrapper) addNewline() {
	w.done = append(w.done, string(w.cur))
	w.cur = w.cur[:0]
	w.curWidth = 0
	w.space = w.space[:0]
	w.spaceWidth = 0
}

func (w *lineWrapper) appendRune(buf []byte, r rune) []byte {
	return utf8.AppendRune(buf, r)
}

// run is the library's loop from offset i of s. With hold set it stops at
// a grapheme cluster that reaches the end of s, returning its offset.
func (w *lineWrapper) run(s string, i int, hold bool) int {
	const nbsp = 0xA0
	for i < len(s) {
		state, action := parser.Table.Transition(w.pstate, s[i])
		if state == parser.Utf8State {
			cluster, width := ansi.FirstGraphemeCluster(s[i:], ansi.GraphemeWidth)
			if hold && i+len(cluster) == len(s) {
				return i
			}
			i += len(cluster)

			r, _ := utf8.DecodeRuneInString(cluster)
			switch {
			case r != utf8.RuneError && unicode.IsSpace(r) && r != nbsp:
				w.addWord()
				w.space = w.appendRune(w.space, r)
				w.spaceWidth += width
			default:
				// The library's breakpoint case never matches here: kiln
				// wraps with no breakpoints.
				if w.wordLen+width > w.limit {
					w.addWord()
				}
				w.word = append(w.word, cluster...)
				w.wordLen += width
				if w.curWidth+w.wordLen+w.spaceWidth > w.limit {
					w.addNewline()
				}
				if w.wordLen == w.limit {
					w.addWord()
				}
			}
			w.pstate = parser.GroundState
			continue
		}

		switch action {
		case parser.PrintAction, parser.ExecuteAction:
			switch r := rune(s[i]); {
			case r == '\n':
				// Unreachable for a line split on "\n"; kept as the library has it.
				if w.wordLen == 0 {
					if w.curWidth+w.spaceWidth > w.limit {
						w.curWidth = 0
					} else {
						w.cur = append(w.cur, w.space...)
					}
					w.space = w.space[:0]
					w.spaceWidth = 0
				}
				w.addWord()
				w.addNewline()
			case unicode.IsSpace(r):
				w.addWord()
				w.space = w.appendRune(w.space, r)
				w.spaceWidth++
			case r == '-':
				w.addSpace()
				if w.curWidth+w.wordLen >= w.limit {
					w.word = w.appendRune(w.word, r)
					w.wordLen++
				} else {
					w.addWord()
					w.cur = w.appendRune(w.cur, r)
					w.curWidth++
				}
			default:
				if w.curWidth == w.limit {
					w.addNewline()
				}
				w.word = w.appendRune(w.word, r)
				w.wordLen++
				if w.wordLen == w.limit {
					w.addWord()
				}
				if w.curWidth+w.wordLen+w.spaceWidth > w.limit {
					w.addNewline()
				}
			}
		default:
			w.word = append(w.word, s[i])
		}

		if w.pstate != parser.Utf8State {
			w.pstate = state
		}
		i++
	}
	return i
}

// finish is the library's flush once the input ends.
func (w *lineWrapper) finish() {
	if w.wordLen == 0 {
		if w.curWidth+w.spaceWidth > w.limit {
			w.curWidth = 0
		} else {
			w.cur = append(w.cur, w.space...)
		}
		w.space = w.space[:0]
		w.spaceWidth = 0
	}
	w.addWord()
}
