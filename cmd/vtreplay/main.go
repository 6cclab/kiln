// Command vtreplay replays a .rec recording produced by
// internal/testkit/screen's WithRecord (raw PTY output bytes, with
// "\x00IN:<text>" markers recording each keystroke/text send) through the
// same VT emulator the harness's own tests use
// (github.com/charmbracelet/x/vt), and prints the final screen in
// internal/testkit/screen's styled golden encoding (see styles.go).
//
// It exists to produce the Claude Code reference styles goldens under
// testdata/reference/claude-code/*.styles.txt: the plain-text *.txt
// screens in that directory come from harness-drive's SCREEN dump, which
// discards colour, but the *.rec files hold the raw, colour-carrying
// bytes. vtreplay is the tool that turns those bytes back into the same
// per-cell style information SCREEN --styles reports for the harness
// itself, so the two are comparable.
//
// Parsing a .rec file: a marker is the four bytes "\x00IN:" (a NUL byte
// controlling programs never emit, so it cannot occur in real PTY output)
// followed immediately by the literal text that was sent (Screen.Send's
// unescaped text, or "<name>" for Screen.SendKey). There is no explicit
// end-of-marker byte; observed recordings (see
// testdata/reference/claude-code/manual-session.rec) show the marker text
// is always plain, ESC-free bytes and is always immediately followed by
// the real PTY response, which always begins with an ESC (0x1b) byte
// (Claude Code and the harness both open every repaint with a cursor/mode
// escape sequence). vtreplay therefore treats "up to the first 0x1b byte
// after the marker" as the marker's text and everything from that 0x1b
// byte to the next marker (or EOF) as PTY output to feed the emulator.
// This is a recording-format assumption, not a documented contract of
// WithRecord; if a future recording's output ever begins with a
// non-ESC byte, this heuristic will misparse it and vtreplay will report
// a garbled screen (visibly wrong, not silently wrong).
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"

	"github.com/andrepato/harness/internal/testkit/screen"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, out, errOut *os.File) int {
	fs := flag.NewFlagSet("vtreplay", flag.ContinueOnError)
	cols := fs.Int("cols", 100, "terminal width the recording was captured at")
	rows := fs.Int("rows", 40, "terminal height the recording was captured at")
	frames := fs.Bool("frames", false, "also print the screen at each \\x00IN: marker, before that keystroke is applied")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(errOut, "usage: vtreplay [--cols N] [--rows N] [--frames] <recording.rec>")
		return 2
	}

	data, err := os.ReadFile(fs.Arg(0)) //nolint:gosec
	if err != nil {
		fmt.Fprintln(errOut, "vtreplay:", err)
		return 1
	}

	segs := parseRecording(data)

	emu := vt.NewEmulator(*cols, *rows)
	// The emulator answers device queries (cursor position reports, DA,
	// etc.) by writing the reply into its own io.Reader side; nothing
	// reads a recorded byte stream back, but an unread reply still blocks
	// the emulator's internal pipe until it is drained. Discard it, the
	// same role screen.go's "encoded keys -> PTY" goroutine plays for a
	// live process.
	go func() { _, _ = io.Copy(io.Discard, emu) }() //nolint:errcheck
	for i, seg := range segs {
		if *frames && i > 0 {
			fmt.Fprintf(out, "=== frame before %q ===\n", segs[i].label)
			fmt.Fprintln(out, dumpStyled(emu, *cols, *rows))
			fmt.Fprintln(out)
		}
		if len(seg.output) > 0 {
			_, _ = emu.Write(seg.output) //nolint:errcheck
		}
	}

	fmt.Fprintln(out, dumpStyled(emu, *cols, *rows))
	return 0
}

// segment is one span of the recording: the marker text that opened it
// (empty for the very first, pre-marker span) and the raw output bytes
// that followed it up to the next marker.
type segment struct {
	label  string
	output []byte
}

const marker = "\x00IN:"

// parseRecording splits raw recording bytes into segments as described in
// the package doc comment.
func parseRecording(data []byte) []segment {
	var segs []segment
	idx := 0
	// Leading span before the first marker, if any.
	first := strings.Index(string(data), marker)
	if first == -1 {
		return []segment{{output: data}}
	}
	segs = append(segs, segment{output: data[:first]})
	idx = first

	for idx < len(data) {
		rest := data[idx:]
		if !strings.HasPrefix(string(rest), marker) {
			break
		}
		textStart := idx + len(marker)
		esc := indexByte(data, textStart, 0x1b)
		nextMarker := strings.Index(string(data[textStart:]), marker)
		if nextMarker >= 0 {
			nextMarker += textStart
		}
		textEnd := esc
		if textEnd == -1 || (nextMarker != -1 && nextMarker < textEnd) {
			// No ESC before the next marker (or EOF): the whole remainder
			// up to the next marker/EOF is marker text with no output.
			if nextMarker != -1 {
				textEnd = nextMarker
			} else {
				textEnd = len(data)
			}
		}
		label := string(data[textStart:textEnd])

		outputStart := textEnd
		outputEnd := len(data)
		if nextMarker != -1 {
			outputEnd = nextMarker
		}
		segs = append(segs, segment{label: label, output: data[outputStart:outputEnd]})

		if nextMarker == -1 {
			break
		}
		idx = nextMarker
	}
	return segs
}

func indexByte(data []byte, from int, b byte) int {
	for i := from; i < len(data); i++ {
		if data[i] == b {
			return i
		}
	}
	return -1
}

// dumpStyled renders emu's current grid using the same encoding
// screen.EncodeStyledScreen uses, so goldens produced here are directly
// comparable to SCREEN --styles output from harness-drive and
// Screen.GoldenStyles from Go tests.
func dumpStyled(emu *vt.Emulator, cols, rows int) string {
	texts := make([]string, rows)
	styles := make([][]screen.CellStyle, rows)
	for y := 0; y < rows; y++ {
		var b strings.Builder
		row := make([]screen.CellStyle, cols)
		var last screen.CellStyle
		for x := 0; x < cols; x++ {
			c := emu.CellAt(x, y)
			if c == nil {
				b.WriteByte(' ')
				row[x] = screen.CellStyle{}
				continue
			}
			if c.Content == "" {
				if c.Width == 0 {
					row[x] = last
					continue
				}
				b.WriteByte(' ')
				row[x] = screen.CellStyle{}
				continue
			}
			b.WriteString(c.Content)
			st := cellStyle(c)
			row[x] = st
			last = st
		}
		texts[y] = b.String()
		styles[y] = row
	}
	return screen.EncodeStyledScreen(texts, styles)
}

func cellStyle(c *uv.Cell) screen.CellStyle {
	return screen.CellStyle{
		Fg:        hexColor(c.Style.Fg),
		Bg:        hexColor(c.Style.Bg),
		Bold:      c.Style.Attrs&uv.AttrBold != 0,
		Dim:       c.Style.Attrs&uv.AttrFaint != 0,
		Italic:    c.Style.Attrs&uv.AttrItalic != 0,
		Underline: c.Style.Underline != uv.UnderlineNone,
		Reverse:   c.Style.Attrs&uv.AttrReverse != 0,
	}
}

func hexColor(c interface{ RGBA() (r, g, b, a uint32) }) string {
	if c == nil {
		return ""
	}
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
}
