package harness

import (
	"fmt"
	"strings"

	"github.com/andrepato/harness/internal/compaction"
	"github.com/andrepato/harness/internal/msg"
)

// truncationMarkerFmt is appended to a text block that got capped. It names
// both the shown and the true size so the model can act on it (e.g. ask for
// the file/command's output a different way) rather than silently reasoning
// over a partial result it thinks is complete.
const truncationMarkerFmt = "\n[truncated: %d of %d estimated tokens shown; the full output was %d tokens]"

// truncateToolResult caps a tool result's text content at budgetTokens,
// estimated with compaction.EstimateBlocks (the same estimator the harness
// uses everywhere else to reason about a message's size, so this cap and
// the tier's own accounting agree). This is the harness's second line of
// defence: individual tools (bash.go's line/byte limits, execenv's spill)
// already cap their own output, but those limits are fixed and
// model-agnostic, so a 32k-window model can still receive a tool result far
// larger than its tier's ToolOutputTokens ceiling. This catches that case
// regardless of which tool produced the content.
//
// budgetTokens <= 0 means unlimited: content is returned unchanged. Image
// blocks are never truncated, only text blocks; a result with only images,
// or whose text already fits, is returned unchanged (same slice, not a
// copy) so the common case allocates nothing.
//
// Truncation keeps the HEAD of each text block (matching the tools' own
// head-truncation rationale: the start of a command's output or a file is
// usually the most useful part) and appends truncationMarkerFmt so the
// model can tell the content was cut and by how much.
func truncateToolResult(content msg.Blocks, budgetTokens int) msg.Blocks {
	if budgetTokens <= 0 || len(content) == 0 {
		return content
	}

	total := compaction.EstimateBlocks(content)
	if total <= budgetTokens {
		return content
	}

	// Split the budget across text blocks by their share of the total
	// estimated tokens, so a result with several text blocks (rare, but
	// legal) doesn't let one block starve the others.
	textTokens := 0
	for _, b := range content {
		if t, ok := b.(msg.TextContent); ok {
			textTokens += compaction.EstimateBlocks(msg.Blocks{t})
		}
	}
	if textTokens == 0 {
		// Nothing to truncate (images only); the estimate overshoot must
		// come from non-text/image blocks EstimateBlocks ignores, or from
		// image cost alone exceeding budget, which we don't truncate.
		return content
	}

	out := make(msg.Blocks, len(content))
	for i, b := range content {
		t, ok := b.(msg.TextContent)
		if !ok {
			out[i] = b
			continue
		}
		blockTokens := compaction.EstimateBlocks(msg.Blocks{t})
		if blockTokens == 0 {
			out[i] = b
			continue
		}
		share := budgetTokens * blockTokens / textTokens
		if share < 1 {
			share = 1
		}
		out[i] = msg.Text(truncateTextToTokens(t.Text, share, blockTokens))
	}
	return out
}

// truncateTextToTokens keeps the head of text that fits within
// budgetTokens (estimated via compaction.EstimateBlocks), cutting on a line
// boundary, then appends a marker recording how much was shown against
// totalTokens (the untruncated estimate). A budget too small for even the
// first line still yields that line, so the marker never claims 0 tokens
// were shown for non-empty input.
func truncateTextToTokens(text string, budgetTokens, totalTokens int) string {
	lines := strings.Split(text, "\n")
	var kept []string
	used := 0
	for i, line := range lines {
		cost := compaction.EstimateBlocks(msg.Blocks{msg.Text(line)})
		if i > 0 {
			cost++ // account for the newline joining it to the previous line
		}
		if len(kept) > 0 && used+cost > budgetTokens {
			break
		}
		kept = append(kept, line)
		used += cost
	}
	if len(kept) == 0 && len(lines) > 0 {
		kept = lines[:1]
	}

	shown := strings.Join(kept, "\n")
	shownTokens := compaction.EstimateBlocks(msg.Blocks{msg.Text(shown)})
	return shown + fmt.Sprintf(truncationMarkerFmt, shownTokens, totalTokens, totalTokens)
}
