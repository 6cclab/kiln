package screen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
)

// CellStyle is the per-cell style information the parity suite compares
// against Claude Code's captured colours. It is a flattened, comparable copy
// of github.com/charmbracelet/ultraviolet's Style: no interfaces, so two
// CellStyles can be compared with ==.
type CellStyle struct {
	Fg        string // "#rrggbb", or "" for the terminal's default foreground
	Bg        string // "#rrggbb", or "" for the terminal's default background
	Bold      bool
	Dim       bool
	Italic    bool
	Underline bool
	Reverse   bool
}

// IsZero reports whether the style carries no information at all (the
// terminal's default rendition).
func (c CellStyle) IsZero() bool {
	return c == CellStyle{}
}

// hex converts a color.Color (as returned by uv.Style's Fg/Bg, which may be
// any of ansi's BasicColor/ExtendedColor/TrueColor implementations, or nil)
// to a lowercase "#rrggbb" string, dropping alpha. nil yields "".
func hexColor(c interface{ RGBA() (r, g, b, a uint32) }) string {
	if c == nil {
		return ""
	}
	r, g, b, _ := c.RGBA()
	// color.Color's RGBA returns 16-bit-per-channel, alpha-premultiplied
	// values; every concrete color this codebase produces is opaque, so
	// truncating to the high byte is a plain 8-bit downsample.
	return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
}

// Styles returns the style of every cell in the current viewport, one row
// of s.rows entries each s.cols wide, in the same top-left-origin order as
// Viewport(). Continuation cells of a wide rune repeat the style of the
// rune's leading cell.
func (s *Screen) Styles() [][]CellStyle {
	s.emuMu.Lock()
	defer s.emuMu.Unlock()

	out := make([][]CellStyle, s.rows)
	for y := 0; y < s.rows; y++ {
		row := make([]CellStyle, s.cols)
		var last CellStyle
		for x := 0; x < s.cols; x++ {
			c := s.emu.CellAt(x, y)
			if c == nil {
				row[x] = CellStyle{}
				continue
			}
			if c.Width == 0 {
				// Continuation column of a wide rune: same style as the
				// cell that started it.
				row[x] = last
				continue
			}
			st := CellStyle{
				Fg:        hexColor(c.Style.Fg),
				Bg:        hexColor(c.Style.Bg),
				Bold:      c.Style.Attrs&uv.AttrBold != 0,
				Dim:       c.Style.Attrs&uv.AttrFaint != 0,
				Italic:    c.Style.Attrs&uv.AttrItalic != 0,
				Underline: c.Style.Underline != uv.UnderlineNone,
				Reverse:   c.Style.Attrs&uv.AttrReverse != 0,
			}
			row[x] = st
			last = st
		}
		out[y] = row
	}
	return out
}

// EncodeStyledRow renders one row of text alongside its per-cell styles as
// a compact, stable golden encoding: runs of cells sharing an identical
// style become one span. A span with no style at all (CellStyle{}) is
// written as plain text with no brackets; a styled span is wrapped
// "[attrs]text[/]" where attrs is a space-joined list of tokens:
//
//	fg=#rrggbb   non-default foreground
//	bg=#rrggbb   non-default background
//	b            bold
//	d            dim/faint
//	i            italic
//	u            underline
//	r            reverse video
//
// Literal '[', ']' and '\' bytes inside text are escaped as '\[', '\]',
// '\\' so a span's boundary is never ambiguous. text is the row's plain
// content (as Screen.Viewport()'s row would render it, i.e. one rune per
// cell, trailing spaces kept); it must have the same cell count as styles.
func EncodeStyledRow(text string, styles []CellStyle) string {
	runes := []rune(padOrTruncate(text, len(styles)))
	var b strings.Builder
	i := 0
	for i < len(styles) {
		j := i + 1
		for j < len(styles) && styles[j] == styles[i] {
			j++
		}
		span := string(runes[i:j])
		if styles[i].IsZero() {
			b.WriteString(escapeSpan(span))
		} else {
			b.WriteByte('[')
			b.WriteString(styleTokens(styles[i]))
			b.WriteByte(']')
			b.WriteString(escapeSpan(span))
			b.WriteString("[/]")
		}
		i = j
	}
	return b.String()
}

func padOrTruncate(s string, n int) string {
	r := []rune(s)
	if len(r) >= n {
		return string(r[:n])
	}
	return s + strings.Repeat(" ", n-len(r))
}

func styleTokens(c CellStyle) string {
	var toks []string
	if c.Fg != "" {
		toks = append(toks, "fg="+c.Fg)
	}
	if c.Bg != "" {
		toks = append(toks, "bg="+c.Bg)
	}
	if c.Bold {
		toks = append(toks, "b")
	}
	if c.Dim {
		toks = append(toks, "d")
	}
	if c.Italic {
		toks = append(toks, "i")
	}
	if c.Underline {
		toks = append(toks, "u")
	}
	if c.Reverse {
		toks = append(toks, "r")
	}
	return strings.Join(toks, " ")
}

func escapeSpan(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `[`, `\[`)
	s = strings.ReplaceAll(s, `]`, `\]`)
	return s
}

// EncodeStyledScreen renders every row (text from Viewport-shaped rows,
// styles from Styles()) as EncodeStyledRow, one per line, trailing blank
// (fully unstyled, all-space) rows trimmed the same way Rows() trims plain
// text.
func EncodeStyledScreen(rows []string, styles [][]CellStyle) string {
	lines := make([]string, len(rows))
	for y := range rows {
		lines[y] = EncodeStyledRow(rows[y], styles[y])
	}
	for len(lines) > 0 && strings.TrimRight(lines[len(lines)-1], " ") == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// GoldenStyles compares the current viewport's style encoding (via
// EncodeStyledScreen over Viewport()/Styles()) against
// testdata/golden/<name>.styles.txt. Set UPDATE=1 to rewrite the golden
// file instead of comparing, mirroring Golden.
func (s *Screen) GoldenStyles(tb testing.TB, name string) {
	tb.Helper()
	got := EncodeStyledScreen(s.Viewport(), s.Styles())
	path := filepath.Join(repoTestdataGoldenDir(), name+".styles.txt")

	if os.Getenv("UPDATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			tb.Fatalf("screen.GoldenStyles: mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(got+"\n"), 0o644); err != nil { //nolint:gosec
			tb.Fatalf("screen.GoldenStyles: write: %v", err)
		}
		return
	}

	want, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		tb.Fatalf("screen.GoldenStyles: read %s: %v (run with UPDATE=1 to create it)", path, err)
	}
	if got+"\n" != string(want) && got != string(want) {
		tb.Errorf("screen.GoldenStyles: %s mismatch\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}
