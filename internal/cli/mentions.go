// `@path` mentions.
//
// Ported from src/tui/mentions.ts. pi-tui's autocomplete completes the path
// into the line; what happens on submit is inlining the file so the model
// has it without spending a turn on a read.
//
// Inlining is capped at the tier's ToolOutputTokens, split evenly across
// the mentions on the line, because it is unconditional spend paid before
// the model can decide it needs the file. Over the cap, the head of the
// file is inlined and the model is told plainly that it was cut.
package cli

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/compaction"
	"github.com/andrepato/harness/internal/msg"
)

// mentionPunct is the set of trailing punctuation excluded from a mention's
// path, matching the MENTION regex's `(?=[.,;:!?)]*(?:\s|$))` lookahead:
// `@src/foo.ts.` and `@src/foo.ts,` are how people write inside a sentence.
const mentionPunct = ".,;:!?)"

// ParseMentions returns the distinct `@path` tokens in line, in first-seen
// order. Ported from mentions.ts's MENTION regex
// `/(?:^|\s)@([^\s@]+?)(?=[.,;:!?)]*(?:\s|$))/g`, since Go's RE2 has no
// lookahead: a mention starts at an `@` preceded by whitespace or the start
// of the line (so an email address's `@`, preceded by a word character,
// never matches), runs to the next whitespace/`@`/end, and then has any
// trailing run of mentionPunct characters stripped (down to a minimum of
// one character, which is what the regex's non-greedy capture group would
// leave for a token made entirely of such punctuation).
func ParseMentions(line string) []string {
	var out []string
	seen := map[string]bool{}
	runes := []rune(line)
	n := len(runes)
	for i := 0; i < n; i++ {
		if runes[i] != '@' {
			continue
		}
		if i != 0 && !unicode.IsSpace(runes[i-1]) {
			continue
		}
		j := i + 1
		for j < n && runes[j] != '@' && !unicode.IsSpace(runes[j]) {
			j++
		}
		token := trimTrailingMentionPunct(string(runes[i+1 : j]))
		if token != "" && !seen[token] {
			seen[token] = true
			out = append(out, token)
		}
		i = j - 1
	}
	return out
}

// trimTrailingMentionPunct strips a trailing run of mentionPunct
// characters, leaving at least one character.
func trimTrailingMentionPunct(token string) string {
	runes := []rune(token)
	n := len(runes)
	m := 0
	for m < n-1 && strings.ContainsRune(mentionPunct, runes[n-1-m]) {
		m++
	}
	return string(runes[:n-m])
}

// imageExtensions maps a lowercase file extension to the mime type a
// vision model expects, matching mentions.ts's IMAGE_TYPES.
var imageExtensions = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// ImageInfo is a mention that resolved to an image, kept separately from
// Mention.Content so it is attached rather than inlined as text.
type ImageInfo struct {
	Data     string // base64, as the provider expects.
	MimeType string
	Bytes    int
}

// Mention is one `@path` token and how it resolved.
type Mention struct {
	// Raw is the text as typed, without the `@`.
	Raw string
	// Path is the resolved absolute path.
	Path string
	// Skipped is why it was not inlined, when it was not.
	Skipped string
	// Content is the inlined content, already capped. Meaningful only when
	// Skipped == "" and Image == nil (a resolved text file, even an empty
	// one, always has Content set - possibly to "").
	Content   string
	Truncated bool
	Tokens    int
	// Image is set when the mention resolved to an image rather than text.
	Image *ImageInfo
}

func (m Mention) resolved() bool {
	return m.Skipped == "" && m.Image == nil
}

// Options configures ResolveMentions.
type Options struct {
	Cwd  string
	Tier *budget.Tier
	// Roots are workspace roots. A mention outside them is not inlined.
	Roots []string
}

// Resolved is the outcome of resolving every `@path` mention on a line.
type Resolved struct {
	// Prompt is the text to send: the original line plus any attached
	// files.
	Prompt string
	// Images to send alongside the prompt, in mention order.
	Images   []msg.ImageContent
	Mentions []Mention
}

// countTokens matches mentions.ts's countTokens: estimateTokens wraps plain
// text in a user message, because pi's estimator counts a message, not a
// string.
func countTokens(text string) int {
	return compaction.EstimateTokens(msg.UserMessage{Content: msg.Blocks{msg.Text(text)}})
}

// capToTokens caps text to budget tokens, cutting on a line boundary.
// Ported from mentions.ts's capToTokens.
func capToTokens(text string, budgetTokens int) (string, bool) {
	if countTokens(text) <= budgetTokens {
		return text, false
	}
	lines := strings.Split(text, "\n")
	var kept []string
	used := 0
	for _, line := range lines {
		// +1 for the newline. Counting per line rather than binary-searching
		// the whole string keeps the cut on a boundary a reader can make
		// sense of.
		cost := countTokens(line) + 1
		if used+cost > budgetTokens {
			break
		}
		kept = append(kept, line)
		used += cost
	}
	// A budget too small for even the first line still has to yield
	// something, or the mention silently vanishes.
	if len(kept) == 0 {
		first := []rune(lines[0])
		if len(first) > 200 {
			first = first[:200]
		}
		kept = append(kept, string(first))
	}
	return strings.Join(kept, "\n"), true
}

// withinRoots reports whether path lies inside any of roots, using the same
// rule as the permission gate (internal/claude/permission.Gate.WithinRoots):
// the relative path from a root is either "." (the root itself) or does not
// start with "..".
func withinRoots(path string, roots []string) bool {
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			continue
		}
		if rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)) {
			return true
		}
	}
	return false
}

// describeReadError turns a stat/read error into the same "not found" vs.
// verbatim message split mentions.ts's catch block makes on
// NodeJS.ErrnoException.code === "ENOENT".
func describeReadError(err error) string {
	if errors.Is(err, fs.ErrNotExist) {
		return "not found"
	}
	return err.Error()
}

// ResolveMentions reads the mentioned files and builds the prompt to send.
// Failures are reported, never returned as an error: a typo'd path
// degrades to "the model did not get that file" with a visible note, not a
// failed turn. The error return exists for the signature the caller
// expects; ResolveMentions itself never fails.
func ResolveMentions(line string, opts Options) (Resolved, error) {
	raws := ParseMentions(line)
	if len(raws) == 0 {
		return Resolved{Prompt: line}, nil
	}

	// Split the budget across mentions so `@a @b @c` cannot cost three
	// times what one mention is allowed to.
	perMention := opts.Tier.ToolOutputTokens / len(raws)
	if perMention < 256 {
		perMention = 256
	}

	mentions := make([]Mention, 0, len(raws))
	for _, raw := range raws {
		path := raw
		if !filepath.IsAbs(path) {
			path = filepath.Join(opts.Cwd, path)
		} else {
			path = filepath.Clean(path)
		}
		m := Mention{Raw: raw, Path: path}

		// The same rule the permission gate applies: a path outside the
		// workspace is not read just because the user typed it quickly.
		// `/add-dir` is the way to widen it.
		if len(opts.Roots) > 0 && !withinRoots(path, opts.Roots) {
			m.Skipped = "outside the workspace"
			mentions = append(mentions, m)
			continue
		}

		info, err := os.Stat(path)
		if err != nil {
			m.Skipped = describeReadError(err)
			mentions = append(mentions, m)
			continue
		}
		if info.IsDir() {
			// A directory listing is a different thing from a file's
			// contents, and inlining one is rarely what was meant.
			m.Skipped = "is a directory"
			mentions = append(mentions, m)
			continue
		}

		// An image is attached, not inlined. Checked before reading,
		// because reading a PNG as UTF-8 produces megabytes of replacement
		// characters.
		if mimeType, ok := imageExtensions[strings.ToLower(filepath.Ext(path))]; ok {
			data, err := os.ReadFile(path)
			if err != nil {
				m.Skipped = describeReadError(err)
				mentions = append(mentions, m)
				continue
			}
			m.Image = &ImageInfo{
				Data:     base64.StdEncoding.EncodeToString(data),
				MimeType: mimeType,
				Bytes:    len(data),
			}
			mentions = append(mentions, m)
			continue
		}

		data, err := os.ReadFile(path)
		if err != nil {
			m.Skipped = describeReadError(err)
			mentions = append(mentions, m)
			continue
		}
		text, truncated := capToTokens(string(data), perMention)
		m.Content = text
		m.Truncated = truncated
		m.Tokens = countTokens(text)
		mentions = append(mentions, m)
	}

	var images []msg.ImageContent
	for _, m := range mentions {
		if m.Image != nil {
			images = append(images, msg.Image(m.Image.MimeType, m.Image.Data))
		}
	}

	var attached []Mention
	for _, m := range mentions {
		if m.resolved() {
			attached = append(attached, m)
		}
	}
	if len(attached) == 0 {
		return Resolved{Prompt: line, Mentions: mentions, Images: images}, nil
	}

	blocks := make([]string, 0, len(attached))
	for _, m := range attached {
		shown := relativeOrRaw(opts.Cwd, m.Path, m.Raw)
		note := ""
		if m.Truncated {
			note = fmt.Sprintf("\n[truncated to fit the context budget - use the read tool for the rest of %s]", shown)
		}
		blocks = append(blocks, fmt.Sprintf("<file path=\"%s\">\n%s%s\n</file>", shown, m.Content, note))
	}

	// Files first, question last: the instruction is what the model should
	// be holding when it starts generating, and trailing content is
	// weighted more heavily by every model this has to run on.
	prompt := strings.Join(blocks, "\n\n") + "\n\n" + line
	return Resolved{Prompt: prompt, Mentions: mentions, Images: images}, nil
}

// relativeOrRaw mirrors mentions.ts's `relative(cwd, path) || raw`.
func relativeOrRaw(cwd, path, raw string) string {
	if path == "" {
		return raw
	}
	rel, err := filepath.Rel(cwd, path)
	if err != nil || rel == "" {
		return raw
	}
	return rel
}

// DescribeMentions renders one transcript line per mention: the path and
// token estimate, whether it was truncated, why it was skipped, or its
// size for an image. The content is not echoed - the user has the file.
// Ported from mentions.ts:210-218.
func DescribeMentions(mentions []Mention, cwd string) []string {
	out := make([]string, 0, len(mentions))
	for _, m := range mentions {
		shown := relativeOrRaw(cwd, m.Path, m.Raw)
		switch {
		case m.Skipped != "":
			out = append(out, fmt.Sprintf("  @%s - %s", shown, m.Skipped))
		case m.Image != nil:
			kb := int(math.Round(float64(m.Image.Bytes) / 1024))
			out = append(out, fmt.Sprintf("  @%s (image, %d KB)", shown, kb))
		default:
			trunc := ""
			if m.Truncated {
				trunc = ", truncated"
			}
			out = append(out, fmt.Sprintf("  @%s (~%d tokens%s)", shown, m.Tokens, trunc))
		}
	}
	return out
}
