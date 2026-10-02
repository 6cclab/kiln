package tui

import (
	"context"
	"strconv"
	"strings"

	"github.com/andrepato/harness/internal/execenv"
)

// The `!` input prefix, ported from input-modes.ts (parity spec §2). `/`
// and `@` are not modes in this sense: `/` is the command registry
// (inputmodes.go's caller runs registry.Execute directly) and `@` mentions
// are resolved as part of an ordinary prompt, not classified here. Claude
// Code's own prompt input has no `#` memory shortcut — its reference
// source's input modes are only bash (`!`) and prompt; a `#`-memory-note
// mode never existed there. kiln dropped its own `#` mode to match: a line
// starting with `#` is now an ordinary prompt.
//
// The distinction that matters: `!` acts directly and never reaches the
// model. On a local model at ~20 tok/s that is the difference between a
// second and a minute.

// InputMode discriminates ClassifyInput's result.
type InputMode string

const (
	ModeBang InputMode = "bang"
)

// Classified is what ClassifyInput returns for a `!` line.
type Classified struct {
	Mode InputMode
	Body string
}

// ClassifyInput inspects a submitted line. The zero Classified (Mode ==
// "") means "send it to the model" — matching classifyInput's
// `undefined` return.
func ClassifyInput(line string) (Classified, bool) {
	if strings.HasPrefix(line, "!") && len(line) > 1 {
		return Classified{Mode: ModeBang, Body: strings.TrimSpace(line[1:])}, true
	}
	return Classified{}, false
}

// bangCaptureMaxBytes and bangCaptureMaxLines bound `!command` output,
// matching runBang's own 64KB/2000-line capture.
const (
	bangCaptureMaxBytes = 64 * 1024
	bangCaptureMaxLines = 2000
)

// RunBang runs command in env directly — the user's own shell, not a tool
// call — and returns the transcript lines to commit: a "shell" block, its
// label rule carrying the exit status when it failed, then the output the
// way a tool block shows a result. The command itself is not repeated:
// the "you" block above already shows it. The output is never sent to the
// model: if the user wants the model to see it, they reference it in
// their next message.
func RunBang(ctx context.Context, command string, env *execenv.Env, width int) []string {
	block := func(meta string, colour func(string) string, body []string) []string {
		out := []string{"", labelRule("shell", Muted, meta, width)}
		for i, l := range body {
			prefix := continuationIndent
			if i == 0 {
				prefix = Muted("→") + " "
			}
			out = append(out, prefix+colour(l))
		}
		return out
	}
	if env == nil {
		return block("", KilnRed, []string{"no shell available"})
	}
	result, err := env.Exec(ctx, command, execenv.ExecOptions{
		Capture: execenv.CaptureLimits{MaxBytes: bangCaptureMaxBytes, MaxLines: bangCaptureMaxLines},
	})
	if err != nil {
		return block("", KilnRed, []string{err.Error()})
	}

	body := strings.TrimRight(result.Text, "\n")
	var lines []string
	if body != "" {
		lines = strings.Split(body, "\n")
	} else {
		// A silent success is ambiguous — say so rather than leaving a
		// bare prompt.
		lines = []string{Muted("(no output)")}
	}
	if result.ExitCode != 0 {
		return block("exit "+strconv.Itoa(result.ExitCode), func(s string) string { return s }, lines)
	}
	return block("", func(s string) string { return s }, lines)
}
