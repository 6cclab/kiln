package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// longSession is n turns of a user line and a ~1.5 KB markdown reply.
func longSession(n int) []session.Entry {
	reply := strings.Repeat("Some **bold** prose with `code` and a [link](https://x.dev).\n\n- one\n- two\n\n", 18)
	var out []session.Entry
	for i := 0; i < n; i++ {
		out = append(out,
			session.Entry{ID: fmt.Sprintf("u%d", i), Seq: int64(2 * i), Type: session.EntryMessage, Message: msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("question")}}},
			session.Entry{ID: fmt.Sprintf("a%d", i), Seq: int64(2*i + 1), Type: session.EntryMessage, Message: msg.AssistantMessage{Role: msg.RoleAssistant, Content: msg.Blocks{msg.Text(fmt.Sprintf("Reply %d.\n\n%s", i, reply))}}},
		)
	}
	return out
}

// BenchmarkReplayLongSession is one Ctrl+O / resize redraw of a 200-turn
// session at a fixed width.
func BenchmarkReplayLongSession(b *testing.B) {
	withRenderEnv(b, 100)
	entries := longSession(200)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		RenderTranscriptEntries(entries, 100, i%2 == 0, "/tmp", nil, nil)
	}
}

// TestReplay_ReusesRenderedProse: a second redraw of the same session at
// the same width (Ctrl+O toggling verbosity) renders no markdown again.
// Each replay used to build its own renderer, so every redraw re-rendered
// all of a session's prose.
func TestReplay_ReusesRenderedProse(t *testing.T) {
	withRenderEnv(t, 100)
	entries := longSession(5)
	first := RenderTranscriptEntries(entries, 100, false, "/tmp", nil, nil)
	before := markdownRenders.Load()
	second := RenderTranscriptEntries(entries, 100, true, "/tmp", nil, nil)
	if n := markdownRenders.Load() - before; n != 0 {
		t.Errorf("second replay rendered %d markdown blocks again, want 0", n)
	}
	if strings.Join(first, "\n") != strings.Join(second, "\n") {
		t.Errorf("replay from cache differs from the first render")
	}

	// A new width or theme renders afresh.
	before = markdownRenders.Load()
	RenderTranscriptEntries(entries, 90, false, "/tmp", nil, nil)
	if markdownRenders.Load() == before {
		t.Errorf("a replay at a new width served the old width's rows")
	}
}

// TestMarkdownRenderer_CacheIsBounded: past its byte budget the cache
// starts over instead of growing.
func TestMarkdownRenderer_CacheIsBounded(t *testing.T) {
	withRenderEnv(t, 80)
	r := &MarkdownRenderer{width: 80, maxBytes: 4 << 10, cache: map[string][]string{}}
	for i := 0; i < 200; i++ {
		r.Render(fmt.Sprintf("reply %d with **some** words in it", i))
	}
	if r.cacheSize > r.maxBytes || len(r.cache) >= 200 {
		t.Errorf("cache holds %d entries, %d bytes; want it bounded by %d bytes", len(r.cache), r.cacheSize, r.maxBytes)
	}
}
