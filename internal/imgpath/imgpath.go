// Package imgpath turns a dragged/pasted file-system path — as a terminal
// delivers it, backslash-escaped and possibly quoted — into an attached
// image, matching Claude Code's own behaviour for a dropped screenshot.
//
// It is a small, dependency-free leaf package (only internal/msg) so both
// internal/cli (the `@path` mention attachment path, internal/cli/mentions.go)
// and internal/tui (bracketed paste, internal/tui/app.go) can use the same
// cleaning and extension rules without internal/tui importing internal/cli
// (which would cycle back through internal/cli's own import of internal/tui).
package imgpath

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/andrepato/harness/internal/msg"
)

// MaxImageBytes bounds a dragged/pasted image path Resolve will actually
// read and base64-encode: above this it is refused the same way a
// RefusedExtensions entry is, rather than read into memory regardless of
// size and handed to a provider that will reject it anyway. 3.75 MiB
// keeps the base64-encoded attachment (which is ~4/3 the raw size) under
// the Anthropic API's 5 MiB base64 image limit — the same derivation
// Claude Code's own image-paste path uses (apiLimits.ts's
// IMAGE_TARGET_RAW_SIZE = API_IMAGE_MAX_BASE64_SIZE * 3/4), checked here
// from the same os.Stat Resolve already does, before any read.
const MaxImageBytes = 5 * 1024 * 1024 * 3 / 4

// pathTokenRe matches a candidate path token starting at "/" or "~": a run
// of characters that are each either a backslash escape (`\X`, consuming
// both characters so an escaped space never ends the token) or anything
// that is not ASCII whitespace. Go's RE2 \s is ASCII-only by default, so
// this does not stop at a macOS screenshot name's U+202F either — only a
// real word boundary (a raw space/tab/newline) ends a token.
var pathTokenRe = regexp.MustCompile(`(?:~|/)(?:\\.|[^\s])*`)

// ResolveEmbedded finds every `/...`/`~/...` token in line that resolves
// (via Resolve) to an attachable image, and replaces each with
// "[Image #N]" (N continuing from startIndex+1), matching Claude Code's
// placeholder for an attached image. Returns the line unchanged (with no
// images) when nothing in it resolves — a line with an ordinary path that
// happens to start with "/" but isn't an image, or isn't on disk, is left
// exactly as typed.
func ResolveEmbedded(line, cwd string, startIndex int) (string, []msg.ImageContent) {
	matches := pathTokenRe.FindAllStringIndex(line, -1)
	if len(matches) == 0 {
		return line, nil
	}
	var images []msg.ImageContent
	var b strings.Builder
	last := 0
	n := startIndex
	for _, loc := range matches {
		raw := line[loc[0]:loc[1]]
		v, ok := Resolve(raw, cwd)
		if !ok || v.Image == nil {
			continue
		}
		b.WriteString(line[last:loc[0]])
		n++
		fmt.Fprintf(&b, "[Image #%d]", n)
		images = append(images, *v.Image)
		last = loc[1]
	}
	if len(images) == 0 {
		return line, nil
	}
	b.WriteString(line[last:])
	return b.String(), images
}

// LeadingTokenIsPath reports whether line's first whitespace-separated
// token (which must start with "/" for the caller to even ask) is a
// path rather than a command name: it contains a second "/" or a
// backslash escape after the leading "/", or it names a file that
// actually exists on disk. Mirrors Claude Code's rule that an absolute
// path typed (or dragged) at the start of the prompt is never run as a
// slash command.
func LeadingTokenIsPath(line, cwd string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "/") {
		return false
	}
	token := pathTokenRe.FindString(trimmed)
	if token == "" {
		return false
	}
	body := strings.TrimPrefix(token, "/")
	if strings.ContainsAny(body, "/\\") {
		return true
	}
	if body == "" {
		return false
	}
	return IsExistingFile(token, cwd)
}

// Extensions maps a lowercase file extension to the mime type a vision
// model expects. Kept in sync with internal/cli/mentions.go's own
// imageExtensions (the `@path` attachment path) rather than importing one
// from the other, since internal/tui cannot import internal/cli.
var Extensions = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// RefusedExtensions names an image extension kiln recognises but does not
// attach as-is: .bmp is rarely accepted by a vision model's API, and kiln
// has no image codec to convert it, so it is refused rather than sent and
// rejected by the provider. Value is the reason shown to the user.
var RefusedExtensions = map[string]string{
	".bmp": "bmp images aren't supported - convert to png or jpeg first",
}

// CleanPathToken turns a whitespace-separated token as a terminal/shell
// delivers a dragged or pasted path into the literal path it names:
// trimmed, outer matching quotes removed, and shell backslash escapes
// resolved (`\ ` -> ` `, `\\` -> `\`, and more generally `\X` -> `X` for
// any escaped character — a dragged macOS path escapes only the ASCII
// spaces in its name). Every other character, including a macOS
// screenshot's U+202F NARROW NO-BREAK SPACE before "AM"/"PM", is kept
// exactly: that character is never escaped by the terminal, so unescaping
// must not touch it either.
func CleanPathToken(token string) string {
	token = strings.TrimSpace(token)
	if n := len(token); n >= 2 {
		if (token[0] == '"' && token[n-1] == '"') || (token[0] == '\'' && token[n-1] == '\'') {
			token = token[1 : n-1]
		}
	}
	runes := []rune(token)
	var b strings.Builder
	b.Grow(len(token))
	for i := 0; i < len(runes); i++ {
		if runes[i] == '\\' && i+1 < len(runes) {
			b.WriteRune(runes[i+1])
			i++
			continue
		}
		b.WriteRune(runes[i])
	}
	return b.String()
}

// HasUnescapedWhitespace reports whether s contains a raw ASCII
// space/tab/newline not preceded by a backslash escape — i.e. whether s is
// more than one shell word. A macOS screenshot name's U+202F is
// deliberately not ASCII whitespace, so it never counts: splitting on it
// would cut the filename in half.
func HasUnescapedWhitespace(s string) bool {
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '\\' && i+1 < len(runes) {
			i++
			continue
		}
		switch runes[i] {
		case ' ', '\t', '\n', '\r':
			return true
		}
	}
	return false
}

// Verdict is what Resolve decided about a candidate path.
type Verdict struct {
	// CleanPath is the resolved, cleaned, absolute path.
	CleanPath string
	// Image is set when the path resolved to an attachable image.
	Image *msg.ImageContent
	// Bytes is the image's size on disk, set alongside Image.
	Bytes int
	// Refused explains why a recognised image extension was not attached
	// (e.g. .bmp). Image is nil when this is set.
	Refused string
}

// imageExt reports the mime type for path's extension, whether it is a
// recognised-but-refused extension (reason non-empty), or neither.
func imageExt(path string) (mime string, refused string) {
	ext := strings.ToLower(filepath.Ext(path))
	if m, ok := Extensions[ext]; ok {
		return m, ""
	}
	if reason, ok := RefusedExtensions[ext]; ok {
		return "", reason
	}
	return "", ""
}

// Resolve cleans raw as a path token (CleanPathToken), resolves it against
// cwd if relative, and reports whether it names an image file that
// exists. ok is false whenever raw is not worth treating specially: not an
// image-shaped extension at all, or the file does not exist — in both
// cases the caller's only correct move is to leave the text exactly as
// typed, matching Claude Code's "if the file doesn't exist, the text
// stays as typed".
func Resolve(raw, cwd string) (Verdict, bool) {
	cleaned := CleanPathToken(raw)
	if cleaned == "" {
		return Verdict{}, false
	}
	path := cleaned
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	} else {
		path = filepath.Clean(path)
	}
	mime, refused := imageExt(path)
	if mime == "" && refused == "" {
		return Verdict{}, false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return Verdict{}, false
	}
	if refused != "" {
		return Verdict{CleanPath: path, Refused: refused}, true
	}
	if info.Size() > MaxImageBytes {
		return Verdict{
			CleanPath: path,
			Refused:   fmt.Sprintf("image is %.1fMB, more than the %.1fMB limit - attach a smaller image", float64(info.Size())/(1<<20), float64(MaxImageBytes)/(1<<20)),
		}, true
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Verdict{CleanPath: path, Refused: err.Error()}, true
	}
	img := msg.Image(mime, base64.StdEncoding.EncodeToString(data))
	return Verdict{CleanPath: path, Image: &img, Bytes: len(data)}, true
}

// IsExistingFile reports whether cleaned path (already CleanPathToken'd,
// and resolved against cwd if relative) names a file that exists — used
// by the command-vs-path classifier (a leading token that exists on disk
// is a path, never a command name), independent of whether it is an
// image.
func IsExistingFile(raw, cwd string) bool {
	cleaned := CleanPathToken(raw)
	if cleaned == "" {
		return false
	}
	path := cleaned
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	} else {
		path = filepath.Clean(path)
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
