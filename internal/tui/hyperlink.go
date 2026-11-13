package tui

import (
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Hyperlink wraps label in an OSC 8 terminal hyperlink to url so a supporting
// terminal makes label clickable. Terminals that don't support OSC 8 ignore the
// escape and show label unchanged, and the escape is zero-width (ansi.StringWidth
// and ansi.Wrap, the engines behind VisibleWidth and ansiWrap, both account for
// it), so a link never disturbs layout or wrapping. A no-op when styling is
// disabled (NO_COLOR, plain/screen-reader mode, piped output) — the same
// `enabled` switch as every other style helper — so escapes never reach output
// that should carry none. An empty url returns label unchanged.
//
// Assistant text (markdown) does not go through here: glamour already emits OSC
// 8 for both bare URLs and [text](url) links. This is for kiln's own renderers
// that build a link outside glamour, i.e. a mod <a href> element (renderLink).
func Hyperlink(url, label string) string {
	if !enabled || url == "" {
		return label
	}
	return ansi.SetHyperlink(url) + label + ansi.ResetHyperlink()
}

// bareURL matches an http(s) URL in plain text, for rows whose link carries
// no OSC 8 (tool output, a URL glamour did not autolink).
var bareURL = regexp.MustCompile(`https?://[^\s<>"'` + "`" + `]+`)

// linkAt returns the http(s) URL under cell col of the rendered row, or ""
// when there is none: the OSC 8 hyperlink covering that cell first, else a
// bare URL in the row's text. Fullscreen takes the mouse away from the
// terminal, so kiln opens links itself (handleMouseSelect).
func linkAt(row string, col int) string {
	var (
		state  byte
		x      int
		osc    string
		plain  strings.Builder
		cellAt = -1 // byte offset in plain of the clicked cell
	)
	for s := row; len(s) > 0; {
		seq, width, n, next := ansi.DecodeSequence(s, state, nil)
		state = next
		s = s[n:]
		if width == 0 {
			if strings.HasPrefix(seq, "\x1b]8;") {
				osc = osc8Target(seq)
			}
			continue
		}
		if col >= x && col < x+width {
			if osc != "" {
				return webURL(osc)
			}
			cellAt = plain.Len()
		}
		plain.WriteString(seq)
		x += width
	}
	if cellAt < 0 {
		return ""
	}
	text := plain.String()
	for _, loc := range bareURL.FindAllStringIndex(text, -1) {
		u := strings.TrimRight(text[loc[0]:loc[1]], ".,;:!?)]}")
		if cellAt >= loc[0] && cellAt < loc[0]+len(u) {
			return webURL(u)
		}
	}
	return ""
}

// osc8Target is the URI of an OSC 8 sequence ("\x1b]8;params;uri" ended by
// BEL or ST); "" for the sequence that closes a link.
func osc8Target(seq string) string {
	body := strings.TrimPrefix(seq, "\x1b]8;")
	body = strings.TrimSuffix(strings.TrimSuffix(body, "\a"), "\x1b\\")
	if _, uri, ok := strings.Cut(body, ";"); ok {
		return uri
	}
	return ""
}

// webURL returns u when it is an http or https URL, else "": a click opens
// web pages only, never another scheme's handler.
func webURL(u string) string {
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
		return ""
	}
	return u
}

// msgOpened reports the outcome of opening a clicked link.
type msgOpened struct {
	url string
	err error
}

// openURL opens u in the default browser: open on macOS, xdg-open
// elsewhere. HARNESS_OPEN_CMD replaces the opener with a shell command that
// gets the URL as $1 (tests use it so nothing reaches a real browser).
func openURL(u string) tea.Cmd {
	return func() tea.Msg {
		var cmd *exec.Cmd
		switch custom := os.Getenv("HARNESS_OPEN_CMD"); {
		case custom != "":
			cmd = exec.Command("/bin/sh", "-c", custom, "sh", u)
		case runtime.GOOS == "darwin":
			cmd = exec.Command("open", u)
		case runtime.GOOS == "windows":
			cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
		default:
			cmd = exec.Command("xdg-open", u)
		}
		return msgOpened{url: u, err: cmd.Run()}
	}
}

// openedNote is the transient mode-line note after a link click.
func openedNote(msg msgOpened) string {
	if msg.err != nil {
		return "open failed: " + msg.err.Error()
	}
	return "opened " + msg.url
}
