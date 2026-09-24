package execenv

import "strings"

// Truncation limits, mirroring pi's utils/truncate.js: whichever of the
// line limit or the byte limit is hit first wins, and truncation never
// returns a partial line except for TruncateTail's single-oversized-line
// edge case.
const (
	DefaultMaxLines = 2000
	DefaultMaxBytes = 50 * 1024
)

// TruncatedBy names which limit caused truncation, or "" when untruncated.
type TruncatedBy string

const (
	TruncatedByNone  TruncatedBy = ""
	TruncatedByLines TruncatedBy = "lines"
	TruncatedByBytes TruncatedBy = "bytes"
)

// TruncationResult is the outcome of TruncateHead or TruncateTail,
// mirroring pi's TruncationResult (minus nothing — Go has no reason to
// split Content out the way ShellOutputTruncation does; callers that need
// the metadata alone simply ignore Content).
type TruncationResult struct {
	Content               string
	Truncated             bool
	TruncatedBy           TruncatedBy
	TotalLines            int
	TotalBytes            int
	OutputLines           int
	OutputBytes           int
	LastLinePartial       bool
	FirstLineExceedsLimit bool
	MaxLines              int
	MaxBytes              int
}

// FormatSize formats bytes as a human-readable size, mirroring pi's
// formatSize.
func FormatSize(bytes int) string {
	switch {
	case bytes < 1024:
		return itoa(bytes) + "B"
	case bytes < 1024*1024:
		return ftoa1(float64(bytes)/1024) + "KB"
	default:
		return ftoa1(float64(bytes)/(1024*1024)) + "MB"
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func ftoa1(f float64) string {
	// One decimal place, matching JS toFixed(1).
	scaled := int64(f*10 + 0.5)
	whole := scaled / 10
	frac := scaled % 10
	return itoa(int(whole)) + "." + itoa(int(frac))
}

// splitLinesForCounting splits content on "\n" the way pi does: a
// trailing newline does not produce a trailing empty line.
func splitLinesForCounting(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	if strings.HasSuffix(content, "\n") {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// TruncateOptions bounds TruncateHead/TruncateTail. Zero values fall back
// to DefaultMaxLines/DefaultMaxBytes.
type TruncateOptions struct {
	MaxLines int
	MaxBytes int
}

func (o TruncateOptions) resolve() (maxLines, maxBytes int) {
	maxLines = o.MaxLines
	if maxLines == 0 {
		maxLines = DefaultMaxLines
	}
	maxBytes = o.MaxBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxBytes
	}
	return
}

// TruncateHead keeps the first N lines/bytes, suitable for file reads
// where the beginning of the file matters. Mirrors pi's truncateHead. If
// the first line alone exceeds maxBytes, it returns empty content with
// FirstLineExceedsLimit set.
func TruncateHead(content string, opts TruncateOptions) TruncationResult {
	maxLines, maxBytes := opts.resolve()
	totalBytes := len(content)
	lines := splitLinesForCounting(content)
	totalLines := len(lines)

	if totalLines <= maxLines && totalBytes <= maxBytes {
		return TruncationResult{
			Content: content, Truncated: false, TruncatedBy: TruncatedByNone,
			TotalLines: totalLines, TotalBytes: totalBytes,
			OutputLines: totalLines, OutputBytes: totalBytes,
			MaxLines: maxLines, MaxBytes: maxBytes,
		}
	}

	if len(lines) > 0 && len(lines[0]) > maxBytes {
		return TruncationResult{
			Content: "", Truncated: true, TruncatedBy: TruncatedByBytes,
			TotalLines: totalLines, TotalBytes: totalBytes,
			FirstLineExceedsLimit: true,
			MaxLines:              maxLines, MaxBytes: maxBytes,
		}
	}

	var outputLines []string
	outputBytes := 0
	truncatedBy := TruncatedByLines
	for i := 0; i < len(lines) && i < maxLines; i++ {
		line := lines[i]
		lineBytes := len(line)
		if i > 0 {
			lineBytes++
		}
		if outputBytes+lineBytes > maxBytes {
			truncatedBy = TruncatedByBytes
			break
		}
		outputLines = append(outputLines, line)
		outputBytes += lineBytes
	}
	if len(outputLines) >= maxLines && outputBytes <= maxBytes {
		truncatedBy = TruncatedByLines
	}
	outputContent := strings.Join(outputLines, "\n")
	return TruncationResult{
		Content: outputContent, Truncated: true, TruncatedBy: truncatedBy,
		TotalLines: totalLines, TotalBytes: totalBytes,
		OutputLines: len(outputLines), OutputBytes: len(outputContent),
		MaxLines: maxLines, MaxBytes: maxBytes,
	}
}

// TruncateTail keeps the last N lines/bytes, suitable for shell output
// where the end (errors, final results) matters. Mirrors pi's
// truncateTail. May return a partial first line if the last line of the
// original content alone exceeds maxBytes.
func TruncateTail(content string, opts TruncateOptions) TruncationResult {
	maxLines, maxBytes := opts.resolve()
	totalBytes := len(content)
	lines := splitLinesForCounting(content)
	totalLines := len(lines)

	if totalLines <= maxLines && totalBytes <= maxBytes {
		return TruncationResult{
			Content: content, Truncated: false, TruncatedBy: TruncatedByNone,
			TotalLines: totalLines, TotalBytes: totalBytes,
			OutputLines: totalLines, OutputBytes: totalBytes,
			MaxLines: maxLines, MaxBytes: maxBytes,
		}
	}

	var outputLines []string
	outputBytes := 0
	truncatedBy := TruncatedByLines
	lastLinePartial := false
	for i := len(lines) - 1; i >= 0 && len(outputLines) < maxLines; i-- {
		line := lines[i]
		lineBytes := len(line)
		if len(outputLines) > 0 {
			lineBytes++
		}
		if outputBytes+lineBytes > maxBytes {
			truncatedBy = TruncatedByBytes
			if len(outputLines) == 0 {
				truncatedLine := truncateStringToBytesFromEnd(line, maxBytes)
				outputLines = append([]string{truncatedLine}, outputLines...)
				outputBytes = len(truncatedLine)
				lastLinePartial = true
			}
			break
		}
		outputLines = append([]string{line}, outputLines...)
		outputBytes += lineBytes
	}
	if len(outputLines) >= maxLines && outputBytes <= maxBytes {
		truncatedBy = TruncatedByLines
	}
	outputContent := strings.Join(outputLines, "\n")
	return TruncationResult{
		Content: outputContent, Truncated: true, TruncatedBy: truncatedBy,
		TotalLines: totalLines, TotalBytes: totalBytes,
		OutputLines: len(outputLines), OutputBytes: len(outputContent),
		LastLinePartial: lastLinePartial,
		MaxLines:        maxLines, MaxBytes: maxBytes,
	}
}

// truncateStringToBytesFromEnd truncates str to fit within maxBytes,
// keeping the end, without splitting a UTF-8 rune. Go strings are already
// byte sequences, so this only needs to walk backward on rune boundaries.
func truncateStringToBytesFromEnd(str string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(str) <= maxBytes {
		return str
	}
	start := len(str) - maxBytes
	for start < len(str) && isUTF8Continuation(str[start]) {
		start++
	}
	return str[start:]
}

func isUTF8Continuation(b byte) bool {
	return b&0xc0 == 0x80
}
