package tui

import (
	"context"
	"strconv"
	"strings"

	"github.com/andrepato/harness/internal/claude/memory"
	"github.com/andrepato/harness/internal/execenv"
)

// The `!` and `#` input prefixes, ported from input-modes.ts (parity spec
// §2). `/` and `@` are not modes in this sense: `/` is the command
// registry (inputmodes.go's caller runs registry.Execute directly) and `@`
// mentions are resolved as part of an ordinary prompt, not classified
// here.
//
// The distinction that matters: `!` and `#` act directly and never reach
// the model. On a local model at ~20 tok/s that is the difference between
// a second and a minute.

// InputMode discriminates ClassifyInput's result.
type InputMode string

const (
	ModeBang   InputMode = "bang"
	ModeMemory InputMode = "memory"
)

// Classified is what ClassifyInput returns for a `!` or `#` line.
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
	if strings.HasPrefix(line, "#") && len(line) > 1 {
		return Classified{Mode: ModeMemory, Body: strings.TrimSpace(line[1:])}, true
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
// call — and returns the transcript lines to commit. The output is never
// sent to the model: if the user wants the model to see it, they reference
// it in their next message.
func RunBang(ctx context.Context, command string, env *execenv.Env) []string {
	if env == nil {
		return []string{Red("! " + command), Red("  no shell available")}
	}
	result, err := env.Exec(ctx, command, execenv.ExecOptions{
		Capture: execenv.CaptureLimits{MaxBytes: bangCaptureMaxBytes, MaxLines: bangCaptureMaxLines},
	})
	if err != nil {
		return []string{Red("! " + command), Red("  " + err.Error())}
	}

	lines := []string{Dim("!") + " " + command}
	body := strings.TrimRight(result.Text, "\n")
	if body != "" {
		for _, l := range strings.Split(body, "\n") {
			lines = append(lines, "  "+l)
		}
	} else {
		// A silent success is ambiguous — say so rather than leaving a
		// bare prompt.
		lines = append(lines, Dim("  (no output)"))
	}
	if result.ExitCode != 0 {
		lines = append(lines, Red("  exit "+strconv.Itoa(result.ExitCode)))
	}
	return lines
}

// AddMemory appends note to project or user memory (memory.AddMemory picks
// which) and returns the transcript lines to commit.
func AddMemory(note, cwd string) []string {
	target, err := memory.AddMemory(note, cwd)
	if err != nil {
		return []string{Red("# " + err.Error())}
	}
	return []string{Green("#") + " added to " + target, Dim("  applies from the next session")}
}
