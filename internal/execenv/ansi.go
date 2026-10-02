package execenv

import "strings"

// StripControlSequences removes raw terminal control sequences and
// control bytes from text that is about to reach either the model's
// context or a rendered terminal screen: complete CSI sequences (cursor
// moves, SGR colour codes such as the truecolor "\x1b[38;2;r;g;bm" form),
// OSC sequences (title changes, the OSC 52 clipboard write), DCS/SOS/PM/APC
// string sequences, any other recognised two-byte escape, a lone ESC left
// unterminated at the end of a chunk, the Unicode "interlinear annotation"
// range (U+FFF9-FFFB, which some terminals also treat specially), and
// every other C0 control byte except '\n' and '\t'.
//
// Unlike a byte-level C0 filter, this removes each sequence as a whole:
// stripping only the leading ESC byte out of "\x1b[38;2;100;200;50m" would
// leave the literal, unreadable "[38;2;100;200;50m" behind as ordinary
// text. That is the actual failure this function exists to prevent — see
// the harness-side caller in internal/harness/toolout.go and the
// TUI-render caller in internal/tui/transcript.go for the two places that
// must never show that leftover text.
//
// A bare '\r' not immediately followed by '\n' is dropped (no existing
// progress-bar-style carriage-return handling was found elsewhere in the
// codebase to match, so this follows the same "drop it" rule as every
// other non-whitelisted C0 control); '\r\n' collapses to '\n'.
//
// This is a single left-to-right scan (no backtracking, no regexp) so it
// stays linear in the size of large tool output.
func StripControlSequences(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i, n := 0, len(s)
	for i < n {
		c := s[i]
		switch {
		case c == 0x1b: // ESC
			i = skipEscapeSequence(s, i, n)
		case c == '\r':
			// Bare \r is dropped; \r\n collapses to \n (the \r is simply
			// skipped here, leaving the loop to copy the \n on its own
			// next iteration).
			i++
		case c == '\n' || c == '\t':
			b.WriteByte(c)
			i++
		case c < 0x20:
			// Every other C0 control byte.
			i++
		case c == 0xef && i+2 < n && s[i+1] == 0xbf && (s[i+2] == 0xb9 || s[i+2] == 0xba || s[i+2] == 0xbb):
			// UTF-8 encoding of U+FFF9, U+FFFA or U+FFFB.
			i += 3
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// skipEscapeSequence consumes one escape sequence starting at s[i] (where
// s[i] == 0x1b, ESC) and returns the index of the byte following it.
func skipEscapeSequence(s string, i, n int) int {
	i++ // past ESC
	if i >= n {
		// A lone ESC at the very end of a chunk (a truncated stream):
		// consumed by itself, nothing left to scan.
		return i
	}
	switch s[i] {
	case '[':
		// CSI: ESC [ <parameter/intermediate bytes 0x20-0x3f> <final byte
		// 0x40-0x7e>. Covers SGR colour codes and cursor-move sequences
		// alike; everything up to and including the final byte is
		// consumed.
		i++
		for i < n {
			c := s[i]
			i++
			if c >= 0x40 && c <= 0x7e {
				break
			}
		}
		return i
	case ']', 'P', 'X', '^', '_':
		// OSC (']'), DCS ('P'), SOS ('X'), PM ('^'), APC ('_'): a string
		// sequence terminated by BEL (OSC only) or ST (ESC \). If the
		// chunk ends before a terminator, the whole remainder is
		// consumed rather than left behind as leftover text.
		osc := s[i] == ']'
		i++
		for i < n {
			if osc && s[i] == 0x07 {
				i++
				break
			}
			if s[i] == 0x1b && i+1 < n && s[i+1] == '\\' {
				i += 2
				break
			}
			i++
		}
		return i
	default:
		// A generic two-byte escape (charset select, save/restore
		// cursor, full reset, etc.): ESC plus the one byte that follows.
		return i + 1
	}
}
