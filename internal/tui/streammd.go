package tui

import (
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// Live markdown for the streaming "kiln" block (stream.go).
//
// The live block renders the reply's markdown as it arrives, so the rows on
// screen are already the rows the committed block will have: nothing
// reformats when streaming ends. Two things make that hold.
//
// Finished blocks render once. The reply is cut at the last "safe" block
// boundary (a blank line outside a code fence, followed by a line that
// starts a new top-level block); everything before it is rendered and
// cached, and only the block still arriving re-renders on each frame. A
// full render costs about 1ms per KB of reply, so re-rendering the whole
// reply every 50ms stream tick would grow with the reply; the cache keeps a
// frame's cost to the size of the last block. Rendering two blocks apart
// and joining them with one blank row gives the rows glamour gives the two
// together (streammd_test.go holds the two to each other).
//
// A block too big to re-render every frame (one paragraph of many KB, a
// long code block) re-renders only after it has grown by a quarter since
// its last render, or has ended, showing that render meanwhile: its text arrives in
// steps, but the cost per byte stays flat however long the block gets
// (TestStreamCost_FlatInReplyLength), and an older render is still the
// start of the final one.
//
// What is still ambiguous is held back (settle): marker characters at the
// very end (the first half of a closing "**"), a line that is only list,
// heading, quote, rule or fence markers so far, an unfinished link, and a
// table until its last row. Emphasis, strikethrough and code spans left
// open are closed for the render, so "**bol" already shows bold and the
// real closing marker changes nothing. The word being typed is shown: the
// reply often pauses after a word (before a tool call), and a word that
// grows past the row's end wraps the way typed text does.

// streamMarkdown caches the rendered finished blocks of one streaming
// reply at one width. It is shared by every copy of a Model (a pointer
// field), and locked because View may run on a copy.
type streamMarkdown struct {
	mu sync.Mutex

	width int
	gen   int
	r     *MarkdownRenderer
	// text is the reply the cache describes; a text that does not extend
	// it starts over.
	text string
	// doneEnd is the offset in text where the finished blocks end; done
	// holds their rendered rows.
	doneEnd int
	done    []string
	// tail is the last render of the block still arriving, made when it
	// was tailLen bytes long.
	tail    []string
	tailLen int
}

// bigBlock is the size past which the block still arriving re-renders in
// steps rather than every frame (about 8ms of rendering at ~1ms per KB).
const bigBlock = 8 << 10

// tailRows returns the last n rows of the rendered reply so far.
func (s *streamMarkdown) tailRows(text string, width, n int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen := ThemeGeneration(); s.r == nil || s.width != width || s.gen != gen || !strings.HasPrefix(text, s.text) {
		s.width, s.gen = width, gen
		s.r = NewMarkdownRenderer(width, IsPlain())
		s.doneEnd, s.done = 0, nil
	}
	s.text = text
	if cut := safeCut(text, s.doneEnd); cut > s.doneEnd {
		s.done = joinBlocks(s.done, s.r.render(text[s.doneEnd:cut]))
		s.doneEnd = cut
		s.tail, s.tailLen = nil, 0
	}
	size := len(text) - s.doneEnd
	if s.tail == nil || size < bigBlock || size >= s.tailLen+s.tailLen/4 || size < s.tailLen ||
		strings.HasSuffix(text, "\n\n") {
		s.tail, s.tailLen = s.r.render(settle(text[s.doneEnd:])), size
	}
	return tailOf(n, s.done, s.tail)
}

// release drops the cache once the reply has committed.
func (s *streamMarkdown) release() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.text, s.doneEnd, s.done = "", 0, nil
	s.tail, s.tailLen = nil, 0
}

// joinBlocks appends rendered rows b after a, with the blank row glamour
// puts between two top-level blocks. (A horizontal rule brings margins of
// its own that merge with its neighbours', so safeCut never cuts next to
// one.)
func joinBlocks(a, b []string) []string {
	switch {
	case len(b) == 0:
		return a
	case len(a) == 0:
		return append([]string(nil), b...)
	}
	out := make([]string, 0, len(a)+1+len(b))
	return append(append(append(out, a...), ""), b...)
}

// tailOf returns the last n rows of joinBlocks(done, tail) without
// copying all of done.
func tailOf(n int, done, tail []string) []string {
	rows := tail
	if len(done) > 0 && len(tail) > 0 {
		rows = append([]string{""}, tail...)
	}
	if len(rows) >= n {
		return append([]string(nil), rows[len(rows)-n:]...)
	}
	need := n - len(rows)
	if need > len(done) {
		need = len(done)
	}
	out := make([]string, 0, need+len(rows))
	out = append(out, done[len(done)-need:]...)
	return append(out, rows...)
}

var (
	fenceOpen = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")
	// markerOnly is a line that is so far only block markers (list,
	// heading, quote, rule, setext underline, table or fence characters,
	// or an ordered-list number): what it becomes depends on what follows.
	markerOnly    = regexp.MustCompile("^[ \t]*([#>*+=_~|`-]+|[0-9]{1,9}[.)]?)?[ \t]*$")
	listStart     = regexp.MustCompile(`^ {0,3}([*+-]|[0-9]{1,9}[.)])([ \t]|$)`)
	thematicBreak = regexp.MustCompile(`^ {0,3}((-[ \t]*){3,}|(\*[ \t]*){3,}|(_[ \t]*){3,})$`)
	tableRule     = regexp.MustCompile(`^[ \t]*\|?[ \t]*:?-*:?[ \t]*(\|[ \t]*:?-*:?[ \t]*)*\|?[ \t]*$`)
)

// fenceState tracks fenced code blocks line by line.
type fenceState struct{ marker string }

// line feeds one complete line and reports whether it is part of a fence
// (its opening, body or closing line).
func (f *fenceState) line(l string) bool {
	if f.marker != "" {
		t := strings.TrimLeft(l, " ")
		if len(l)-len(t) <= 3 && strings.HasPrefix(t, f.marker) &&
			strings.Trim(t, f.marker[:1]+" \t") == "" {
			f.marker = ""
		}
		return true
	}
	if m := fenceOpen.FindStringSubmatch(l); m != nil {
		if m[1][0] == '`' && strings.Contains(l[len(m[0]):], "`") {
			return false // a backtick fence's info string has no backticks
		}
		f.marker = m[1]
		return true
	}
	return false
}

func blankLine(l string) bool { return strings.TrimSpace(l) == "" }

// safeCut returns the offset of the last safe block boundary in text at or
// after from (itself a boundary, or 0): the start of a line that follows a
// blank line outside any fence, begins in column 0 with a non-blank
// character, does not continue a list that the block before it holds, and
// has no horizontal rule on either side.
func safeCut(text string, from int) int {
	cut := from
	var f fenceState
	afterBlank, inList, afterRule := false, false, false
	for i := from; i < len(text); {
		nl := strings.IndexByte(text[i:], '\n')
		if nl < 0 {
			// The line still streaming can start the next block once it
			// has a character to judge by.
			if afterBlank && !afterRule && f.marker == "" && boundaryLine(text[i:], inList) {
				cut = i
			}
			break
		}
		l := text[i : i+nl]
		if afterBlank && !afterRule && f.marker == "" && boundaryLine(l, inList) {
			cut = i
			inList = false
		}
		inFence := f.line(l)
		switch {
		case inFence:
			afterBlank, afterRule = false, false
		case blankLine(l):
			afterBlank = true
		default:
			afterBlank = false
			afterRule = thematicBreak.MatchString(l)
			if listStart.MatchString(l) && !afterRule {
				inList = true
			}
		}
		i += nl + 1
	}
	return cut
}

func boundaryLine(l string, inList bool) bool {
	if l == "" || l[0] == ' ' || l[0] == '\t' || markerOnly.MatchString(l) {
		return false // markers only: it may still become a rule
	}
	return !(inList && listStart.MatchString(l))
}

// settle returns the part of tail (the reply after the cached blocks) that
// can be rendered now without changing later: see the file comment.
func settle(tail string) string {
	nl := strings.LastIndexByte(tail, '\n')
	body, last := tail[:nl+1], tail[nl+1:]

	// Find where the current block starts, and whether the reply is inside
	// an open code fence.
	var f fenceState
	blockStart := 0
	for i := 0; i < len(body); {
		e := strings.IndexByte(body[i:], '\n')
		l := body[i : i+e]
		wasOpen := f.marker != ""
		inFence := f.line(l)
		if (!inFence && blankLine(l)) || (wasOpen && f.marker == "") {
			blockStart = i + e + 1
		}
		i += e + 1
	}
	if f.marker != "" {
		// Inside code: whole lines only (a partial token can highlight
		// differently once it is complete), with the fence closed.
		return body + f.marker + "\n"
	}
	if fenceOpen.MatchString(last) {
		last = "" // the fence's own line, until it is complete
	}

	// A table: its header row could still be a paragraph, and its column
	// widths depend on every row, so it shows whole once it ends.
	block := body[blockStart:] + last
	// (The second line decides: a delimiter row such as "|---|---|", or
	// while it is the line still arriving, anything that could become one.)
	if lines := strings.Split(block, "\n"); strings.Contains(stripCodeSpans(lines[0]), "|") {
		if len(lines) == 1 || (len(lines) == 2 && tableRule.MatchString(lines[1])) ||
			(tableRule.MatchString(lines[1]) && strings.Contains(lines[1], "-")) {
			return body[:blockStart]
		}
	}

	// Marker characters at the end, then a line that is markers only so far.
	last = strings.TrimRight(last, "*_~`\\")
	if markerOnly.MatchString(last) {
		last = ""
	}
	visible := body + last
	block = visible[blockStart:]
	if blankLine(block) {
		return visible
	}
	block = holdLink(block)
	block = strings.TrimRight(block, " \t\n")
	return visible[:blockStart] + block + inlineClosers(block)
}

// holdLink cuts block at an unfinished link or image: an open "[" whose
// "]" has not arrived, a "]" that may still be followed by "(", or a
// destination whose ")" has not arrived. Until then it could be literal
// text or a link, which render differently.
func holdLink(block string) string {
	var open []int
	for i := 0; i < len(block); i++ {
		switch block[i] {
		case '\\':
			i++
		case '`':
			// Brackets inside a code span are literal.
			n := runLen(block, i, '`')
			if j := strings.Index(block[i+n:], strings.Repeat("`", n)); j >= 0 {
				i += n + j + n - 1
			} else {
				i = len(block)
			}
		case '[':
			open = append(open, i)
		case ']':
			if len(open) == 0 {
				continue
			}
			start := open[len(open)-1]
			open = open[:len(open)-1]
			if start > 0 && block[start-1] == '!' {
				start--
			}
			if i+1 == len(block) {
				return block[:start]
			}
			if block[i+1] == '(' {
				j := strings.IndexByte(block[i+1:], ')')
				if j < 0 {
					return block[:start]
				}
				i += j + 1
			}
		}
	}
	if len(open) > 0 {
		start := open[0]
		if start > 0 && block[start-1] == '!' {
			start--
		}
		return block[:start]
	}
	return block
}

// stripCodeSpans drops closed code spans from a line, so a pipe inside one
// ("`a | b`") does not read as a table cell border.
func stripCodeSpans(l string) string {
	var b strings.Builder
	for i := 0; i < len(l); i++ {
		if l[i] == '`' {
			n := runLen(l, i, '`')
			if j := strings.Index(l[i+n:], strings.Repeat("`", n)); j >= 0 {
				i += n + j + n - 1
				continue
			}
		}
		b.WriteByte(l[i])
	}
	return b.String()
}

func runLen(s string, i int, c byte) int {
	n := 0
	for i+n < len(s) && s[i+n] == c {
		n++
	}
	return n
}

// inlineClosers returns the markers that close the code span, emphasis
// and strikethrough runs still open at the end of block, innermost first,
// following CommonMark's flanking rules closely enough for prose.
func inlineClosers(block string) string {
	var stack []opener
	for i := 0; i < len(block); i++ {
		c := block[i]
		switch c {
		case '\\':
			i++
			continue
		case '`':
			n := runLen(block, i, '`')
			j := strings.Index(block[i+n:], strings.Repeat("`", n))
			if j < 0 {
				// An open code span: nothing after it is parsed.
				return strings.Repeat("`", n) + closersFor(stack)
			}
			i += n + j + n - 1
			continue
		case '*', '_', '~':
		default:
			continue
		}
		n := runLen(block, i, c)
		before, after := runeBefore(block, i), runeAfter(block, i+n)
		left := !isSpace(after) && (!isPunct(after) || isSpace(before) || isPunct(before))
		right := !isSpace(before) && (!isPunct(before) || isSpace(after) || isPunct(after))
		canOpen, canClose := left, right
		if c == '_' {
			canOpen = left && (!right || isPunct(before))
			canClose = right && (!left || isPunct(after))
		}
		closed := false
		if canClose {
			for k := len(stack) - 1; k >= 0; k-- {
				if stack[k].c == c {
					stack = stack[:k]
					closed = true
					break
				}
			}
		}
		if !closed && canOpen {
			stack = append(stack, opener{c, n, isSpace(before) || isPunct(before)})
		}
		i += n - 1
	}
	return closersFor(stack)
}

// opener is an emphasis or strikethrough run still waiting for its closer.
type opener struct {
	c byte
	n int
	// wordStart: the run starts a word. Only those are closed for the
	// render; one inside a word ("2*3", "a~b") is more often literal, and
	// closing it would show emphasis that later goes away.
	wordStart bool
}

// closersFor returns the closing runs for stack's word-start openers,
// innermost first.
func closersFor(stack []opener) string {
	var b strings.Builder
	for k := len(stack) - 1; k >= 0; k-- {
		if stack[k].wordStart {
			b.WriteString(strings.Repeat(string(stack[k].c), stack[k].n))
		}
	}
	return b.String()
}

// runeBefore and runeAfter return the rune next to a delimiter run, or a
// space at either end of the block (CommonMark treats both as whitespace).
func runeBefore(s string, i int) rune {
	if i == 0 {
		return ' '
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return r
}

func runeAfter(s string, i int) rune {
	if i >= len(s) {
		return ' '
	}
	r, _ := utf8.DecodeRuneInString(s[i:])
	return r
}

func isSpace(r rune) bool { return unicode.IsSpace(r) }
func isPunct(r rune) bool { return unicode.IsPunct(r) || unicode.IsSymbol(r) }
