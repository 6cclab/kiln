package tui

import (
	"maps"
	"math/rand"
	"slices"
	"strings"
	"testing"
)

// streamWrapCorpus covers what a streamed reply carries: prose, markdown
// that spans deltas (fences, lists, tables), runs with no break, hyphens,
// wide and multibyte characters, grapheme clusters a split can cut
// (combining marks, ZWJ emoji, skin tones, flags), whitespace the wrapper
// treats specially, and escape sequences.
var streamWrapCorpus = map[string]string{
	"prose":       strings.Repeat(streamCostSentence, 6),
	"paragraphs":  "First paragraph, short.\n\nSecond paragraph is a little longer than the first one was.\n\n\nThird.\n",
	"fence":       "Here is code:\n\n```go\nfunc main() {\n\tfmt.Println(\"hello, world — a long line that will need wrapping at narrow widths\")\n}\n```\n\nAfter the fence.",
	"list":        "- one item\n- two items that run long enough to wrap onto a second row\n  - nested item\n1. numbered\n2. numbered again",
	"table":       "| col a | col b |\n|-------|-------|\n| a long cell value here | b |\n| x | y |",
	"url":         "See https://example.com/a/very/long/path/without/any/breaks/at/all/that/keeps/going?q=1&r=2 for details.",
	"hyphens":     "well-known co-operative state-of-the-art re-entry x-y-z ---- -a- b-",
	"cjk":         "日本語のテキストは幅が二つのセルを使います。中文也是一样的宽度。한국어도 마찬가지입니다。",
	"mixed-wide":  "abc日本def語ghi本jkl 日 本 語 x日y",
	"combining":   "e\u0301te\u0301 cafe\u0301 nai\u0308ve a\u0300\u0301\u0302\u0303 o\u0308\u0308\u0308\u0308\u0308",
	"emoji":       "family 👨\u200d👩\u200d👧\u200d👦 thumbs 👍🏽👍🏿 heart ❤\ufe0f smile ☺\ufe0f done 🎉🎉🎉",
	"flags":       "🇺🇸🇫🇷🇩🇪🇯🇵 odd 🇺🇸🇫 end",
	"hangul-jamo": "\u1100\u1161\u11a8 \u1100\u1161 \u1100",
	"spaces":      "trailing   \nlead   ing\n     \n  \t tabs\tand\r carriage\u00a0nbsp\u00a0run \u2003em\u3000ideo",
	"exact":       "aaaa bbbb cccc dddd eeee ffff gggg hhhh iiii jjjj",
	"ansi":        "plain \x1b[1mbold text that wraps\x1b[0m and \x1b]8;;https://e.x/\x07a link\x1b]8;;\x07 then \x1b[38;5;208mcolour\x1b[m end",
	"c1":          "abc\u009b31mdef ghi\u0085jkl",
	"invalid":     "ok \xff\xfe bytes \xe6\x97 cut",
	"empty-lines": "\n\n\na\n\n",
	"long-word":   strings.Repeat("x", 300) + " " + strings.Repeat("日", 77) + " y",
}

var streamWrapWidths = []int{1, 2, 3, 4, 5, 7, 10, 13, 20, 40, 80}

// streamWrapAll returns every row the cache holds for text.
func streamWrapAll(c *streamWrapCache, text string, width int) ([]string, int) {
	return c.tailRows(text, width, 1<<30)
}

// TestStreamWrapCache_MatchesFullWrapAtEverySplit feeds each corpus text
// one byte at a time — so every delta boundary, including the middle of a
// multibyte rune, an escape sequence or a grapheme cluster, is a split
// point — and holds the cache's rows to wrapMultiline's full re-wrap at
// every step.
func TestStreamWrapCache_MatchesFullWrapAtEverySplit(t *testing.T) {
	for name, text := range streamWrapCorpus {
		for _, width := range streamWrapWidths {
			c := &streamWrapCache{}
			for i := 1; i <= len(text); i++ {
				prefix := text[:i]
				got, total := streamWrapAll(c, prefix, width)
				want := wrapMultiline(prefix, width)
				if !slices.Equal(got, want) || total != len(want) {
					t.Fatalf("%s width=%d after %d bytes:\n got  %q (total %d)\n want %q", name, width, i, got, total, want)
				}
			}
		}
	}
}

// TestStreamWrapCache_RandomChunks streams the whole corpus, joined, in
// random chunk sizes at random widths, and checks the tail at random
// row counts as well as the whole.
func TestStreamWrapCache_RandomChunks(t *testing.T) {
	var all []string
	for _, name := range slices.Sorted(maps.Keys(streamWrapCorpus)) {
		all = append(all, streamWrapCorpus[name])
	}
	text := strings.Join(all, " ")
	rng := rand.New(rand.NewSource(1))
	for round := 0; round < 40; round++ {
		width := 1 + rng.Intn(90)
		c := &streamWrapCache{}
		for i := 0; i < len(text); {
			i = min(len(text), i+1+rng.Intn(24))
			prefix := text[:i]
			want := wrapMultiline(prefix, width)
			n := 1 + rng.Intn(12)
			got, total := c.tailRows(prefix, width, n)
			if !slices.Equal(got, lastRows(want, n)) || total != len(want) {
				t.Fatalf("round %d width=%d after %d bytes, last %d rows:\n got  %q (total %d)\n want %q (total %d)", round, width, i, n, got, total, lastRows(want, n), len(want))
			}
		}
	}
}

// TestStreamWrapCache_ResizeAndRestart covers the cache's resets: a width
// change mid-stream re-wraps at the new width, and a text that does not
// extend the cached one (the next message) starts over.
func TestStreamWrapCache_ResizeAndRestart(t *testing.T) {
	text := strings.Repeat(streamCostSentence, 4) + "\n" + streamWrapCorpus["emoji"]
	c := &streamWrapCache{}
	for i, width := range []int{40, 40, 17, 80, 80, 3} {
		prefix := text[:len(text)*(i+1)/6]
		got, _ := streamWrapAll(c, prefix, width)
		if want := wrapMultiline(prefix, width); !slices.Equal(got, want) {
			t.Fatalf("step %d width=%d:\n got  %q\n want %q", i, width, got, want)
		}
	}
	next := "A new message, not an extension of the last one."
	got, _ := streamWrapAll(c, next, 20)
	if want := wrapMultiline(next, 20); !slices.Equal(got, want) {
		t.Fatalf("new message:\n got  %q\n want %q", got, want)
	}
	c.release()
	got, _ = streamWrapAll(c, next[:10], 20)
	if want := wrapMultiline(next[:10], 20); !slices.Equal(got, want) {
		t.Fatalf("after release:\n got  %q\n want %q", got, want)
	}
}

// TestRenderStreamLive_CachedEqualsUncached holds the live block itself,
// caret and truncation included, to the uncached render.
func TestRenderStreamLive_CachedEqualsUncached(t *testing.T) {
	withRenderEnv(t, 40)
	text := streamWrapCorpus["fence"] + "\n" + streamWrapCorpus["cjk"] + streamWrapCorpus["long-word"]
	for _, width := range []int{1, 8, 40, 81} {
		for _, rows := range []int{0, 1, 8, 30} {
			c := &streamWrapCache{}
			for i := 1; i <= len(text); i += 5 {
				got := renderStreamLiveRows(text[:i], width, rows, c)
				want := RenderStreamLive(text[:i], width, rows)
				if !slices.Equal(got, want) {
					t.Fatalf("width=%d rows=%d after %d bytes:\n got  %q\n want %q", width, rows, i, got, want)
				}
			}
		}
	}
}

// FuzzStreamWrapCache holds the cache to the full re-wrap for any text,
// width and chunking. `go test ./internal/tui -fuzz FuzzStreamWrapCache`
// explores; a plain test run replays the seeds.
func FuzzStreamWrapCache(f *testing.F) {
	for _, text := range streamWrapCorpus {
		f.Add(text, uint8(10), uint8(3))
	}
	f.Fuzz(func(t *testing.T, text string, width, step uint8) {
		w := int(width%100) + 1
		s := int(step%16) + 1
		c := &streamWrapCache{}
		for i := 0; i < len(text); {
			i = min(len(text), i+s)
			got, total := streamWrapAll(c, text[:i], w)
			want := wrapMultiline(text[:i], w)
			if !slices.Equal(got, want) || total != len(want) {
				t.Fatalf("width=%d after %d bytes of %q:\n got  %q\n want %q", w, i, text, got, want)
			}
		}
	})
}
