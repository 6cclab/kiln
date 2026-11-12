package tui

import (
	"math/rand"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// streamReplies are replies shaped like a model's: the markdown the live
// block must render without anything on screen changing later.
var streamReplies = map[string]string{
	"mixed": "## Root cause\n\n" +
		"The `listOrders` handler slices `orders[start:end]` without checking that **start is within range**, so a page past the end panics with *slice bounds out of range*.\n\n" +
		"Steps I took:\n\n" +
		"1. Queried the incidents MCP for **recent errors** on `orders-svc`.\n" +
		"2. Reproduced it with `curl 'localhost:8080/orders?page=3&per_page=10'`.\n" +
		"3. Added a guard and a regression test.\n\n" +
		"- `handlers.go`: clamp `start` to `len(orders)`\n" +
		"- `handlers_test.go`: new `TestListOrders_PastEnd`\n" +
		"  - covers page 3 with 10 per page\n" +
		"  - and an empty store\n\n" +
		"```go\nif start > len(orders) {\n\tstart = len(orders)\n}\n```\n\n" +
		"| file | change |\n|------|--------|\n| handlers.go | guard |\n| handlers_test.go | regression test |\n\n" +
		"> Note: the fix keeps the response shape, see [the API doc](https://example.com/api/orders) for details.\n\n" +
		"Run `go test ./...` to check; snake_case_names and 2 * 3 = 6 stay literal, as does a_b_c.\n\n" +
		"---\n\n" +
		"Done. ~~Old approach~~ replaced, and the `a | b` pipe in code is not a table.",
	"prose": "Paragraph one covers the upload route and why it needed a limiter in the first place, since a single client was able to exhaust the worker pool during a retry storm.\n\n" +
		"Paragraph two covers the middleware itself: a **fixed window counter backed by Redis, keyed by client id**, rejecting once the count crosses ten in a rolling minute.\n\n" +
		"Paragraph three is _emphasised in part_ and ends with a link to https://example.com/limits.",
	"lists": "### Plan\n\n" +
		"3. Start at three\n4. Then four, with a long item that wraps across more than one row when the terminal is narrow\n\n" +
		"* star bullets\n* with `code` and **bold**\n\n" +
		"- loose item one\n\n- loose item two\n\n" +
		"After the lists, an image ![diagram](x.png) and an escaped \\*not italic\\* phrase.",
	"code-first": "```\nplain fence with no language\n\n  indented line after a blank\n```\n\n~~~python\nprint('tilde fence')\n~~~\n\nTrailing text.",
}

// trailingSGR matches style resets at the end of a row: a row cut short
// mid-span ends with the reset its full form has further along.
type styledCell struct {
	r     rune
	style string // the SGR and hyperlink in effect
}

// cells reads a rendered row as characters with the style each is drawn
// in: glamour splits the same styling into different escape runs depending
// on how much text a span has, so rows that look the same can differ as
// strings.
func cells(row string) []styledCell {
	var out []styledCell
	var sgr []string
	link := ""
	for i := 0; i < len(row); {
		if m := sgrSeq.FindString(row[i:]); m != "" {
			if p := m[2 : len(m)-1]; p == "" || p == "0" {
				sgr = nil
			} else {
				sgr = append(sgr, p)
			}
			i += len(m)
			continue
		}
		if m := oscLink.FindStringSubmatch(row[i:]); m != nil {
			link = m[1]
			i += len(m[0])
			continue
		}
		r, size := utf8.DecodeRuneInString(row[i:])
		out = append(out, styledCell{r, strings.Join(sgr, ";") + "|" + link})
		i += size
	}
	return out
}

var (
	sgrSeq  = regexp.MustCompile(`^\x1b\[[0-9;]*m`)
	oscLink = regexp.MustCompile(`^\x1b\]8;[^;]*;([^\x07\x1b]*)(\x07|\x1b\\)`)
)

// trimCells drops trailing spaces: a blockquote pads its rows with spaces
// that have no background, so they draw nothing.
func trimCells(c []styledCell) []styledCell {
	for len(c) > 0 && c[len(c)-1].r == ' ' {
		c = c[:len(c)-1]
	}
	return c
}

// cellsPrefix reports whether live draws the start of final.
func cellsPrefix(live, final string) bool {
	l, f := trimCells(cells(live)), cells(final)
	return len(l) <= len(f) && slices.Equal(l, f[:len(l)])
}

// TestStreamMarkdown_NeverRedraws streams each reply a byte at a time and
// holds every frame to the final committed render: every row but the last
// is already final, and the last is the start of its final row. So nothing
// on screen changes as the reply streams, and nothing reformats when it
// commits.
func TestStreamMarkdown_NeverRedraws(t *testing.T) {
	withRenderEnv(t, 80)
	for name, reply := range streamReplies {
		for _, width := range []int{36, 80} {
			final := NewMarkdownRenderer(width, false).render(reply)
			c := &streamMarkdown{}
			for i := 1; i <= len(reply); i++ {
				assertDrawsStartOf(t, c.tailRows(reply[:i], width, 1<<30), final, name, reply[:i])
			}
			if got := c.tailRows(reply+"\n\n", width, 1<<30); !slices.Equal(got, final) {
				t.Fatalf("%s width=%d: whole reply streamed\n got  %q\n want %q", name, width, got, final)
			}
		}
	}
}

// cut returns row up to its visible column i, styles kept.
func cut(row string, i int) string { return ansi.Cut(row, 0, i) }

// assertDrawsStartOf fails unless live is what final starts with: every
// row but the last the same, and the last the start of its final row.
func assertDrawsStartOf(t *testing.T, live, final []string, name, streamed string) {
	t.Helper()
	if len(live) == 0 {
		return
	}
	if len(live) > len(final) {
		t.Fatalf("%s after %q: %d rows, final has %d\nlive:\n%s", name, streamed, len(live), len(final), strings.Join(live, "\n"))
	}
	n := len(live) - 1
	for r := 0; r < n; r++ {
		if !slices.Equal(trimCells(cells(live[r])), trimCells(cells(final[r]))) {
			t.Fatalf("%s after %q: row %d\n live  %q\n final %q", name, streamed, r, live[r], final[r])
		}
	}
	last := live[n]
	if !strings.HasSuffix(streamed, " ") && !strings.HasSuffix(streamed, "\n") {
		// The word still arriving may grow past the row's end and wrap.
		if plain := ansi.Strip(last); strings.LastIndex(plain, " ") >= 0 {
			last = cut(last, ansi.StringWidth(plain[:strings.LastIndex(plain, " ")]))
		} else {
			last = ""
		}
	}
	if !cellsPrefix(last, final[n]) {
		t.Fatalf("%s after %q: last row %d\n live  %q\n final %q", name, streamed, n, live[n], final[n])
	}
}

// TestStreamMarkdown_BigBlockStepsForward: a paragraph past bigBlock
// re-renders in steps, and every step still draws the start of the final
// render, keeps growing, and the whole paragraph shows once it ends.
func TestStreamMarkdown_BigBlockStepsForward(t *testing.T) {
	withRenderEnv(t, 80)
	reply := strings.Repeat("Some **bold words** and `code` in a long paragraph. ", 3*bigBlock/50)
	final := NewMarkdownRenderer(80, false).render(reply)
	c := &streamMarkdown{}
	renders, prev := 0, -1
	for i := 0; i < len(reply); {
		i = min(len(reply), i+97)
		live := c.tailRows(reply[:i], 80, 1<<30)
		assertDrawsStartOf(t, live, final, "big paragraph", reply[max(0, i-60):i])
		if len(live) < prev {
			t.Fatalf("after %d bytes: %d rows, fewer than the %d before", i, len(live), prev)
		}
		if c.tailLen == i-c.doneEnd {
			renders++
		}
		prev = len(live)
	}
	if renders >= len(reply)/97 {
		t.Errorf("rendered on all %d frames: a block past bigBlock should render in steps", renders)
	}
	if got := c.tailRows(reply+"\n\n", 80, 1<<30); !slices.Equal(got, final) {
		t.Errorf("finished paragraph: %d rows, want the final %d", len(got), len(final))
	}
}

// TestStreamMarkdown_CacheMatchesFreshRender: the cached finished blocks
// plus the block still arriving give the rows a fresh render of the same
// text gives, whatever the chunking, width changes or a new reply.
func TestStreamMarkdown_CacheMatchesFreshRender(t *testing.T) {
	withRenderEnv(t, 80)
	rng := rand.New(rand.NewSource(1))
	for name, reply := range streamReplies {
		c := &streamMarkdown{}
		width := 60
		for i := 0; i < len(reply); {
			i = min(len(reply), i+1+rng.Intn(12))
			if rng.Intn(40) == 0 {
				width = 30 + rng.Intn(60)
			}
			got := c.tailRows(reply[:i], width, 1<<30)
			want := (&streamMarkdown{}).tailRows(reply[:i], width, 1<<30)
			if !slices.Equal(got, want) {
				t.Fatalf("%s width=%d after %d bytes:\n got  %q\n want %q", name, width, i, got, want)
			}
			if tail := c.tailRows(reply[:i], width, 3); !slices.Equal(tail, want[max(0, len(want)-3):]) {
				t.Fatalf("%s: last 3 rows %q, want %q", name, tail, want[max(0, len(want)-3):])
			}
		}
		next := "A new reply, not an extension of the last one."
		if got, want := c.tailRows(next, width, 1<<30), (&streamMarkdown{}).tailRows(next, width, 1<<30); !slices.Equal(got, want) {
			t.Fatalf("%s then a new reply:\n got  %q\n want %q", name, got, want)
		}
	}
}

// TestStreamMarkdown_JoinMatchesWholeRender: the cache renders finished
// blocks apart and joins them with one blank row, which must be what
// glamour gives for the two blocks rendered together.
func TestStreamMarkdown_JoinMatchesWholeRender(t *testing.T) {
	withRenderEnv(t, 80)
	blocks := []string{
		"A paragraph with **bold** and `code`.",
		"# Heading one",
		"### Heading three",
		"- item one\n- item two\n  - nested",
		"1. first\n2. second",
		"```go\nfunc f() {}\n```",
		"> quoted text",
		"| a | b |\n|---|---|\n| 1 | 2 |",
		"---",
	}
	r := NewMarkdownRenderer(60, false)
	for _, a := range blocks {
		for _, b := range blocks {
			if safeCut(a+"\n\n"+b, 0) == 0 {
				continue // never cut there: a list the next block continues
			}
			want := r.render(a + "\n\n" + b)
			if got := joinBlocks(r.render(a), r.render(b)); !slices.Equal(got, want) {
				t.Errorf("%q then %q:\n joined %q\n whole  %q", a, b, got, want)
			}
		}
	}
}

func TestSettle(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"word being typed shows", "hello wor", "hello wor"},
		{"bold left open", "**two wor", "**two wor**"},
		{"half a closer", "**bold*", "**bold**"},
		{"bold across a closed span", "a **b** and *c d", "a **b** and *c d*"},
		{"code span left open", "run `go test ./", "run `go test ./`"},
		{"nested open", "**bold _and it", "**bold _and it_**"},
		{"strikethrough", "~~old appr", "~~old appr~~"},
		{"snake_case is not emphasis", "use snake_case_names her", "use snake_case_names her"},
		{"lone star is not emphasis", "2 * 3 = 6 and mo", "2 * 3 = 6 and mo"},
		{"star inside a word is not closed", "2*3 is si", "2*3 is si"},
		{"unfinished link text", "see [the do", "see"},
		{"unfinished link destination", "see [doc](https://exa", "see"},
		{"finished link", "see [doc](https://example.com) and mo", "see [doc](https://example.com) and mo"},
		{"bracket that may become a link", "see [doc]", "see"},
		{"bracket then a space is literal", "see [doc] and mo", "see [doc] and mo"},
		{"list marker only", "intro\n\n- ", "intro\n\n"},
		{"ordered marker only", "intro\n\n12.", "intro\n\n"},
		{"heading marker only", "## ", ""},
		{"setext or rule underline", "Title\n--", "Title"},
		{"open fence", "text\n\n```go\nfunc f() {\n\treturn", "text\n\n```go\nfunc f() {\n```\n"},
		{"fence line being typed", "text\n\n```py", "text\n\n"},
		{"closed fence", "```\na\n```\n\nafter it wor", "```\na\n```\n\nafter it wor"},
		{"blank line in a fence is not a block end", "```\na\n\nb **c\n", "```\na\n\nb **c\n```\n"},
		{"table header", "intro\n\n| a | b |\n", "intro\n\n"},
		{"table rows", "intro\n\n| a | b |\n|---|---|\n| 1 | 2 |\n", "intro\n\n"},
		{"finished table", "| a | b |\n|---|---|\n| 1 | 2 |\n\n", "| a | b |\n|---|---|\n| 1 | 2 |\n\n"},
		{"pipe inside code is not a table", "use `a | b` to pip", "use `a | b` to pip"},
		{"paragraph line with a pipe", "a | b and mo", ""},
		{"escaped star", "\\*not italic\\* he", "\\*not italic\\* he"},
	} {
		if got := settle(tc.in); got != tc.want {
			t.Errorf("%s: settle(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestSafeCut(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		want     int
	}{
		{"no boundary yet", "one paragraph", 0},
		{"next block started", "one\n\ntwo", 5},
		{"next block not started", "one\n\n", 0},
		{"blank line inside a fence", "```\na\n\nb\n```\n", 0},
		{"after a fence", "```\na\n\nb\n```\n\nnext", 14},
		{"indented continuation", "- a\n\n  more of a", 0},
		{"list continues after a blank", "- a\n\n- b", 0},
		{"list ends", "- a\n\nprose", 5},
		{"rule before", "a\n\n---\n\nb", 0},
		{"rule after", "a\n\n---", 0},
		{"past a rule", "a\n\n---\n\nb\n\nc", 11},
	} {
		if got := safeCut(tc.in, 0); got != tc.want {
			t.Errorf("%s: safeCut(%q) = %d, want %d", tc.name, tc.in, got, tc.want)
		}
	}
}
