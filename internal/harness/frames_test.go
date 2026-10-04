package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/session/jsonl"
)

// A streamed reply records its block boundaries as pending frames, not one
// line per token: the end frame carries the block's whole text. Per-token
// frames were 96% of a long session file's lines, kept after the reply
// committed because the file is append-only.
func TestStreamedReplyWritesNoDeltaFrames(t *testing.T) {
	text := strings.Repeat("many small chunks of streamed text ", 40)
	rig := newTestRig(t, "model: faux-1\nsteps:\n  - text: \""+text+"\"\n", []string{"bash"})
	if _, err := rig.mustLane("main").Prompt(context.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	var types []string
	fullText := ""
	for i, line := range readRawLines(t, rig.Path) {
		if i == 0 {
			continue
		}
		writes, err := jsonl.ParseTransaction([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range writes {
			if w.Value == nil || w.Value.Namespace != session.NamespacePendingAssistantFrame || w.Value.Op != "append" {
				continue
			}
			var f Frame
			if err := json.Unmarshal(w.Value.Value, &f); err != nil {
				t.Fatal(err)
			}
			types = append(types, string(f.Type))
			if f.Type == "text_end" {
				fullText = f.Content
			}
		}
	}
	for _, ty := range types {
		if strings.HasSuffix(ty, "_delta") {
			t.Fatalf("frames %v include per-token deltas", types)
		}
	}
	if len(types) == 0 || len(types) > 4 {
		t.Fatalf("frames %v, want the block boundaries only", types)
	}
	if fullText != text {
		t.Fatalf("text_end frame carries %q, want the whole block", fullText)
	}
}
