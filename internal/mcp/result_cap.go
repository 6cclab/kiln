package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/andrepato/harness/internal/diag"
	"github.com/andrepato/harness/internal/msg"
)

// MCP tool results are capped as Claude Code caps them
// (code.claude.com/docs/en/mcp.md, "MCP output limits"): a successful
// text result over MAX_MCP_OUTPUT_TOKENS (default 25,000) tokens, or over
// 50,000 characters whatever its token count, is saved to a file and
// replaced in the conversation by a message naming the file, so the model
// reads only the parts it needs. An error result over about 11,000
// characters keeps its first and last 5,000. Results with images are left
// whole, as Claude Code's rule covers results with no image content.
const (
	maxOutputTokensEnv     = "MAX_MCP_OUTPUT_TOKENS"
	defaultMaxOutputTokens = 25000
	maxResultChars         = 50000
	errorKeepChars         = 5000
	errorCapChars          = 11000
	// warnOutputTokens is Claude Code's warning threshold; kiln logs it.
	warnOutputTokens = 10000
	// previewBytes is how much of a saved result the message still shows.
	previewBytes = 2048
)

// maxOutputTokens is MAX_MCP_OUTPUT_TOKENS, or the default when it is
// unset or not a positive integer.
func maxOutputTokens() int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(maxOutputTokensEnv))); err == nil && n > 0 {
		return n
	}
	return defaultMaxOutputTokens
}

// estimateTokens is kiln's chars/4 estimate (compaction.estimateText).
func estimateTokens(chars int) int { return (chars + 3) / 4 }

// SetResultDir sets where oversized results are saved: the session
// scratchpad, which the model reads without a permission prompt. Until it
// is set (or when the session has no scratchpad) they go to the system
// temp directory.
func (h *Hub) SetResultDir(dir string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.resultDir = dir
}

func (h *Hub) getResultDir() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.resultDir
}

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// capResult applies the output limits to one tool call's result blocks.
func capResult(blocks msg.Blocks, isError bool, dir, toolName, callID string) msg.Blocks {
	var text strings.Builder
	for _, b := range blocks {
		switch v := b.(type) {
		case msg.TextContent:
			if text.Len() > 0 {
				text.WriteString("\n")
			}
			text.WriteString(v.Text)
		case msg.ImageContent:
			return blocks
		}
	}
	full := text.String()
	chars := len([]rune(full))
	tokens := estimateTokens(len(full))
	if tokens > warnOutputTokens {
		diag.L().Warn("mcp large output", "tool", toolName, "tokens", tokens, "chars", chars)
	}
	if isError {
		if chars <= errorCapChars {
			return blocks
		}
		r := []rune(full)
		return msg.Blocks{msg.Text(string(r[:errorKeepChars]) +
			fmt.Sprintf("\n\n[… %d characters omitted …]\n\n", chars-2*errorKeepChars) +
			string(r[chars-errorKeepChars:]))}
	}
	if tokens <= maxOutputTokens() && chars <= maxResultChars {
		return blocks
	}
	path, err := saveResult(dir, toolName, callID, full)
	if err != nil {
		diag.L().Warn("mcp large output: save failed", "tool", toolName, "err", err)
		return blocks
	}
	preview := full
	if len(preview) > previewBytes {
		preview = strings.ToValidUTF8(preview[:previewBytes], "")
	}
	// Claude Code's own wording for a result it saved to a file.
	return msg.Blocks{msg.Text(fmt.Sprintf(
		"<persisted-output>\nOutput too large (%s). Full output saved to: %s\n\nPreview (first 2KB):\n%s\n...\n</persisted-output>",
		humanSize(len(full)), path, preview))}
}

// humanSize formats n bytes as Claude Code does in that message ("57.8KB").
func humanSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}

// saveResult writes text to a new private file in dir (the system temp
// directory when dir is "") named after the tool and call.
func saveResult(dir, toolName, callID, text string) (string, error) {
	if dir == "" {
		dir = os.TempDir()
	}
	name := unsafeFileChars.ReplaceAllString(toolName+"-"+callID, "_")
	f, err := os.CreateTemp(dir, "mcp-"+name+"-*.txt")
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return filepath.Abs(f.Name())
}
