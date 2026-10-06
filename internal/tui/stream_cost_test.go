package tui

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The per-delta cost of a streaming reply (finding
// 20261006T111858Z-stream-render-cost-grows-with-reply): every message the
// program receives runs Update and View, so whatever the live region does
// per frame is done once per text delta. These tests and benchmarks feed a
// Model the same MsgStreamText sequence the bridge sends and measure the
// work per delta against the reply's length and the transcript's.

const streamCostChunk = "filler. "

// streamCostSentence is prose with ordinary word breaks, one source line
// when repeated (the shape of the context-overflow faux reply).
const streamCostSentence = "This filler text exists only to grow the measured context size past kilns compaction threshold. "

// streamCostModel builds a Model at 120x40 with transcriptRows committed
// rows (each a plain 100-column row), fullscreen or inline.
func streamCostModel(fullscreen bool, transcriptRows int) Model {
	m := NewModel(Config{
		Cwd:         "/tmp",
		ModelLabel:  "faux/faux-1",
		InitialMode: "manual",
		StartedAt:   time.Unix(0, 0),
		Fullscreen:  fullscreen,
	})
	m.width, m.height = 120, 40
	if transcriptRows > 0 {
		row := strings.Repeat("x", 100)
		rows := make([]string, transcriptRows)
		for i := range rows {
			rows[i] = fmt.Sprintf("%5d %s", i, row)
		}
		m = m.appendTranscript(rows)
	}
	if m.fullscreen {
		m = m.layoutViewport()
	}
	return m
}

// streamCostText is n bytes of reply: prose on one line.
func streamCostText(n int) string {
	return strings.Repeat(streamCostSentence, n/len(streamCostSentence)+1)[:n]
}

// newStreamBuf returns a builder holding text with room for growth, so the
// deltas appended to it do not themselves copy the reply (the bridge
// accumulates the reply in a strings.Builder the same way).
func newStreamBuf(text string, extra int) *strings.Builder {
	b := &strings.Builder{}
	b.Grow(len(text) + extra)
	b.WriteString(text)
	return b
}

// streamDeltas feeds k deltas onto buf, one Update and one View each, the
// way the program loop handles a MsgStreamText.
func streamDeltas(m Model, buf *strings.Builder, k int) Model {
	for i := 0; i < k; i++ {
		buf.WriteString(streamCostChunk)
		next, _ := m.Update(MsgStreamText{Text: buf.String()})
		m = next.(Model)
		_ = m.View()
	}
	return m
}

// bytesPerDelta measures heap bytes allocated per delta after the reply
// has reached replyLen bytes. Allocation is a deterministic proxy for the
// work a frame does (every wrap, join and split allocates its output), so
// unlike wall-clock time it does not flake under load.
func bytesPerDelta(fullscreen bool, transcriptRows, replyLen int) float64 {
	const warm, k = 20, 200
	m := streamCostModel(fullscreen, transcriptRows)
	buf := newStreamBuf(streamCostText(replyLen), (warm+k)*len(streamCostChunk))
	// Warm up: the first frame at this length builds the caches.
	m = streamDeltas(m, buf, warm)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	streamDeltas(m, buf, k)
	runtime.ReadMemStats(&after)
	return float64(after.TotalAlloc-before.TotalAlloc) / k
}

// TestStreamCost_FlatInReplyLength fails while a frame re-wraps (or
// re-joins) the whole reply: at 150k bytes a delta must cost about what it
// costs at 10k.
func TestStreamCost_FlatInReplyLength(t *testing.T) {
	if testing.Short() {
		t.Skip("allocation measurement")
	}
	for _, fullscreen := range []bool{false, true} {
		t.Run(fmt.Sprintf("fullscreen=%v", fullscreen), func(t *testing.T) {
			small := bytesPerDelta(fullscreen, 0, 10_000)
			large := bytesPerDelta(fullscreen, 0, 150_000)
			t.Logf("bytes/delta: reply 10k = %.0f, reply 150k = %.0f (%.2fx)", small, large, large/small)
			if large > 3*small {
				t.Errorf("a delta at a 150k-byte reply allocates %.0f bytes, %.1fx the %.0f at 10k; want < 3x (per-delta cost must not grow with the reply)", large, large/small, small)
			}
		})
	}
}

// TestStreamCost_FlatInTranscriptLength fails while a fullscreen frame
// rebuilds the whole committed transcript: with 20k committed rows a delta
// must cost about what it costs with none.
func TestStreamCost_FlatInTranscriptLength(t *testing.T) {
	if testing.Short() {
		t.Skip("allocation measurement")
	}
	for _, fullscreen := range []bool{false, true} {
		t.Run(fmt.Sprintf("fullscreen=%v", fullscreen), func(t *testing.T) {
			empty := bytesPerDelta(fullscreen, 0, 10_000)
			long := bytesPerDelta(fullscreen, 20_000, 10_000)
			t.Logf("bytes/delta: transcript 0 rows = %.0f, 20k rows = %.0f (%.2fx)", empty, long, long/empty)
			if long > 3*empty {
				t.Errorf("a delta with 20k committed rows allocates %.0f bytes, %.1fx the %.0f with none; want < 3x (per-delta cost must not grow with the transcript)", long, long/empty, empty)
			}
		})
	}
}

// BenchmarkStreamDelta reports the cost of one streamed delta (Update +
// View) by reply length and committed transcript size.
//
//	go test ./internal/tui -run '^$' -bench StreamDelta -benchtime 500x
func BenchmarkStreamDelta(b *testing.B) {
	for _, fullscreen := range []bool{true, false} {
		for _, rows := range []int{0, 20_000} {
			for _, reply := range []int{10_000, 40_000, 150_000} {
				name := fmt.Sprintf("fullscreen=%v/transcript=%d/reply=%dk", fullscreen, rows, reply/1000)
				b.Run(name, func(b *testing.B) {
					m := streamCostModel(fullscreen, rows)
					buf := newStreamBuf(streamCostText(reply), (b.N+5)*len(streamCostChunk))
					m = streamDeltas(m, buf, 5)
					b.ReportAllocs()
					b.ResetTimer()
					streamDeltas(m, buf, b.N)
				})
			}
		}
	}
}
