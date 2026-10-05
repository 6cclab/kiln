package execenv

import (
	"bufio"
	"io"
)

// ReadWholeFileCap bounds an untargeted read tool call (no offset or
// limit argument): above this, the model is told to page with
// offset/limit, or use bash, instead of kiln reading — and then
// truncating the output of — a file that might be arbitrarily large.
// 256 KiB matches Claude Code's own observed behaviour for its Read
// tool: a whole-file read above that size is refused outright rather
// than read and truncated, so the model pages explicitly instead of
// kiln repeating a large read-and-truncate on every call.
const ReadWholeFileCap int64 = 256 * 1024

// OpenLineScanner opens path for streaming, line-at-a-time reading: a
// regular file only (following symlinks, never blocking on a FIFO or
// device — the same guard ReadFile uses), with no upper size cap of its
// own, since the caller asked to page through it rather than read it
// whole. The returned io.Closer must be closed once scanning is done.
func (e *Env) OpenLineScanner(path string, maxLineBytes int) (*LineScanner, io.Closer, error) {
	resolved := e.AbsolutePath(path)
	f, _, err := openRegular(resolved)
	if err != nil {
		return nil, nil, err
	}
	return NewLineScanner(f, maxLineBytes), f, nil
}

// ScanLine is one line read by LineScanner, stripped of its trailing
// newline (and a preceding "\r", matching splitLinesForCounting's CRLF
// handling). Content holds at most the scanner's maxBytes; Oversize
// reports when the real line was longer — ByteLen is the line's true
// byte length either way (not counting the stripped terminator), so a
// caller can report it (matching TruncateHead's FirstLineExceedsLimit
// message) without ever holding it.
type ScanLine struct {
	Content  string
	ByteLen  int
	Oversize bool
}

// LineScanner reads a file (or any io.Reader) one line at a time, each
// capped at maxBytes: a single line longer than that is drained to find
// its real length but never held past maxBytes. This is what read.go's
// offset/limit path uses so paging through part of a large file never
// requires loading it whole (core audit H1) — Next only ever retains one
// bounded line, regardless of how many lines, or how long one un-newlined
// line, the file holds before or after it.
type LineScanner struct {
	r        *bufio.Reader
	maxBytes int
	done     bool
}

// NewLineScanner wraps r. maxBytes <= 0 falls back to DefaultMaxBytes.
func NewLineScanner(r io.Reader, maxBytes int) *LineScanner {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	return &LineScanner{r: bufio.NewReaderSize(r, 64*1024), maxBytes: maxBytes}
}

// Next returns the next line, or ok=false once the reader is exhausted.
// err is only ever a read error from the underlying reader, never
// bufio.ErrBufferFull or io.EOF (both are handled internally).
func (s *LineScanner) Next() (ScanLine, bool, error) {
	if s.done {
		return ScanLine{}, false, nil
	}
	var buf []byte
	total := 0 // every byte of the line, including a trailing "\n" if any
	oversize := false
	foundDelim := false
	for {
		chunk, err := s.r.ReadSlice('\n')
		if len(chunk) > 0 {
			total += len(chunk)
			if !oversize {
				if len(buf)+len(chunk) > s.maxBytes {
					if room := s.maxBytes - len(buf); room > 0 {
						buf = append(buf, chunk[:room]...)
					}
					oversize = true
				} else {
					buf = append(buf, chunk...)
				}
			}
		}
		switch {
		case err == nil:
			foundDelim = true
		case err == bufio.ErrBufferFull:
			continue
		case err == io.EOF:
			s.done = true
			if total == 0 {
				return ScanLine{}, false, nil
			}
		default:
			return ScanLine{}, false, err
		}
		break
	}

	byteLen := total
	if foundDelim {
		byteLen--
	}
	var text string
	if oversize {
		text = string(buf)
	} else {
		text = string(buf)
		if n := len(text); n > 0 && text[n-1] == '\n' {
			text = text[:n-1]
			if n2 := len(text); n2 > 0 && text[n2-1] == '\r' {
				text = text[:n2-1]
			}
		}
	}
	return ScanLine{Content: text, ByteLen: byteLen, Oversize: oversize}, true, nil
}
