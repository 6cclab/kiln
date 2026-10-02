package execenv

import (
	"strings"
	"testing"
)

func TestStripControlSequencesTable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "sgr color code",
			in:   "\x1b[38;2;100;200;50mhello\x1b[0m",
			want: "hello",
		},
		{
			name: "osc52 clipboard write terminated by BEL",
			in:   "before\x1b]52;c;aGVsbG8=\x07after",
			want: "beforeafter",
		},
		{
			name: "osc52 clipboard write terminated by ST",
			in:   "before\x1b]52;c;aGVsbG8=\x1b\\after",
			want: "beforeafter",
		},
		{
			name: "osc0 title change",
			in:   "before\x1b]0;some title\x07after",
			want: "beforeafter",
		},
		{
			name: "csi erase screen",
			in:   "a\x1b[2Jb",
			want: "ab",
		},
		{
			name: "csi cursor position",
			in:   "a\x1b[10;5Hb",
			want: "ab",
		},
		{
			name: "csi hide cursor (private mode)",
			in:   "a\x1b[?25lb",
			want: "ab",
		},
		{
			name: "dcs sequence",
			in:   "a\x1bPsome dcs payload\x1b\\b",
			want: "ab",
		},
		{
			name: "lone unterminated esc at end of chunk",
			in:   "hello\x1b",
			want: "hello",
		},
		{
			name: "bare cr not followed by newline is dropped",
			in:   "foo\rbar",
			want: "foobar",
		},
		{
			name: "cr followed by newline collapses to newline",
			in:   "foo\r\nbar",
			want: "foo\nbar",
		},
		{
			name: "plain text with newline tab and unicode untouched",
			in:   "héllo\tworld\nこんにちは",
			want: "héllo\tworld\nこんにちは",
		},
		{
			name: "other c0 control bytes dropped",
			in:   "a\x00\x01\x07\x08b",
			want: "ab",
		},
		{
			name: "interlinear annotation range dropped",
			in:   "a" + string(rune(0xFFF9)) + string(rune(0xFFFA)) + string(rune(0xFFFB)) + "b",
			want: "ab",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := StripControlSequences(tc.in)
			if got != tc.want {
				t.Fatalf("StripControlSequences(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestStripControlSequencesTruecolorHalfBlocks reproduces the real
// incident this fix exists for: a script printing thousands of truecolor
// half-block sequences to build terminal art. The stripped output must
// contain no ESC byte and no leftover literal bracket-code text.
func TestStripControlSequencesTruecolorHalfBlocks(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		b.WriteString("\x1b[38;2;100;200;50m▀")
	}
	in := b.String()

	got := StripControlSequences(in)

	if strings.ContainsRune(got, '\x1b') {
		t.Fatalf("stripped output still contains ESC byte")
	}
	if strings.Contains(got, "[38;2;") {
		t.Fatalf("stripped output still contains leftover bracket-code text: %q", truncateForError(got))
	}
	wantBlocks := strings.Repeat("▀", 2000)
	if got != wantBlocks {
		t.Fatalf("stripped output = %q (len %d), want %d half-block runes", truncateForError(got), len(got), 2000)
	}
}

func truncateForError(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
