package harness

import (
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/compaction"
	"github.com/andrepato/harness/internal/msg"
)

// TestTruncateToolResult_UnderBudget checks content well within
// budgetTokens is returned unchanged (same block values, no marker
// appended) -- the common case, where truncation must be a no-op.
func TestTruncateToolResult_UnderBudget(t *testing.T) {
	content := msg.Blocks{msg.Text("small output")}
	got := truncateToolResult(content, 1000)
	if len(got) != 1 {
		t.Fatalf("got %d blocks, want 1", len(got))
	}
	text, ok := got[0].(msg.TextContent)
	if !ok {
		t.Fatalf("got[0] is %T, want TextContent", got[0])
	}
	if text.Text != "small output" {
		t.Errorf("text = %q, want unchanged %q", text.Text, "small output")
	}
	if strings.Contains(text.Text, "[truncated:") {
		t.Errorf("under-budget content should not carry a truncation marker: %q", text.Text)
	}
}

// TestTruncateToolResult_OverBudget checks content well over budgetTokens
// is cut to (approximately) the budget and carries the truncation marker
// naming shown vs. total estimated tokens, with the HEAD of the content
// kept (first line present, last line absent).
func TestTruncateToolResult_OverBudget(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 500; i++ {
		b.WriteString("line ")
		b.WriteString(strings.Repeat("x", 40))
		b.WriteString("\n")
	}
	full := b.String()
	totalTokens := compaction.EstimateBlocks(msg.Blocks{msg.Text(full)})

	const budget = 100
	if totalTokens <= budget {
		t.Fatalf("test setup invalid: totalTokens=%d not > budget=%d", totalTokens, budget)
	}

	content := msg.Blocks{msg.Text(full)}
	got := truncateToolResult(content, budget)
	if len(got) != 1 {
		t.Fatalf("got %d blocks, want 1", len(got))
	}
	text, ok := got[0].(msg.TextContent)
	if !ok {
		t.Fatalf("got[0] is %T, want TextContent", got[0])
	}

	if !strings.HasPrefix(text.Text, "line ") {
		t.Errorf("truncated text does not start with the original head: %q", text.Text[:min(40, len(text.Text))])
	}
	if !strings.Contains(text.Text, "[truncated:") {
		t.Errorf("truncated text missing marker: %q", text.Text)
	}
	wantTail := "[truncated: "
	idx := strings.Index(text.Text, wantTail)
	if idx < 0 {
		t.Fatalf("marker not found")
	}
	marker := text.Text[idx:]
	if !strings.Contains(marker, "the full output was") {
		t.Errorf("marker missing total-tokens phrase: %q", marker)
	}

	// The kept content (everything before the marker) should be
	// noticeably smaller than the original -- the tail line must be gone.
	shownContent := text.Text[:idx]
	if strings.Contains(shownContent, "line 0499") {
		t.Errorf("truncated content still contains the tail, want head-only: over-budget content was not cut")
	}
	shownTokens := compaction.EstimateBlocks(msg.Blocks{msg.Text(shownContent)})
	if shownTokens > budget+5 { // +5 slack: line-boundary cuts, not binary search
		t.Errorf("shown content ~%d tokens, want roughly <= budget %d", shownTokens, budget)
	}
}

// TestTruncateToolResult_ImagesUntouched checks a mixed text+image result
// only truncates the text block; the image block is byte-for-byte
// unchanged, and truncation is still driven off the total estimate
// (EstimateBlocks counts image blocks too, via pi's flat per-image
// estimate).
func TestTruncateToolResult_ImagesUntouched(t *testing.T) {
	img := msg.Image("image/png", "ZmFrZWRhdGE=")
	var b strings.Builder
	for i := 0; i < 300; i++ {
		b.WriteString(strings.Repeat("y", 60))
		b.WriteString("\n")
	}
	content := msg.Blocks{img, msg.Text(b.String())}

	got := truncateToolResult(content, 50)
	if len(got) != 2 {
		t.Fatalf("got %d blocks, want 2", len(got))
	}
	gotImg, ok := got[0].(msg.ImageContent)
	if !ok {
		t.Fatalf("got[0] is %T, want ImageContent", got[0])
	}
	if gotImg != img {
		t.Errorf("image block changed: got %+v, want %+v", gotImg, img)
	}
	text, ok := got[1].(msg.TextContent)
	if !ok {
		t.Fatalf("got[1] is %T, want TextContent", got[1])
	}
	if !strings.Contains(text.Text, "[truncated:") {
		t.Errorf("text block should have been truncated with a marker: %q", text.Text)
	}
}

// TestTruncateToolResult_Unlimited checks budgetTokens <= 0 (harness.
// Options.ToolOutputTokens's zero value) disables truncation entirely,
// even for content that would otherwise be cut -- this is the "not wired
// up yet" / "explicitly unlimited" case.
func TestTruncateToolResult_Unlimited(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		b.WriteString(strings.Repeat("z", 60))
		b.WriteString("\n")
	}
	content := msg.Blocks{msg.Text(b.String())}
	got := truncateToolResult(content, 0)
	if len(got) != 1 {
		t.Fatalf("got %d blocks, want 1", len(got))
	}
	text := got[0].(msg.TextContent)
	if text.Text != b.String() {
		t.Errorf("budgetTokens<=0 should leave content unchanged")
	}
}
