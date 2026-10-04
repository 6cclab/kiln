package tui

import (
	"testing"
	"time"

	"github.com/andrepato/harness/internal/harness"
)

// The compaction progress row says when a part is being retried, and why,
// and keeps saying it is a retry while the retry streams.
func TestBridge_CompactionRetryShownOnProgressRow(t *testing.T) {
	b := NewBridge("/tmp")
	defer b.Stop()
	f := &fakeSink{}
	b.setSink(f)
	h := b.labelHandler()

	h(harness.Event{Type: harness.EventCompactionStart})
	h(harness.Event{Type: harness.EventCompactionRetry, CompactionPart: 1, Attempt: 2, MaxAttempts: 2, RetryError: "the model stopped responding"})
	h(harness.Event{Type: harness.EventCompactionProgress, CompactionModel: "ollama/qwen", CompactionPart: 1, CompactionParts: 2, CompactionPromptTokens: 19073, CompactionOutputTokens: 138})

	want := []string{
		"Compacting conversation",
		"Compacting conversation · part 1: the model stopped responding, retrying",
		"Compacting conversation (part 1 of 2) · ollama/qwen is writing the summary (138 tokens) · retry 2 of 2",
	}
	var got []string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(got) < len(want) {
		got = got[:0]
		_, sent := f.snapshot()
		for _, m := range sent {
			if c, ok := m.(MsgCompaction); ok {
				got = append(got, c.Label)
			}
		}
		time.Sleep(time.Millisecond)
	}
	if len(got) != len(want) {
		t.Fatalf("labels = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("label %d = %q, want %q", i, got[i], want[i])
		}
	}
}
