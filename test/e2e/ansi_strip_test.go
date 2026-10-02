//go:build e2e

package e2e

// Regression coverage for the raw-ANSI-escape-sequence UX failure: a real
// session where a model ran a python script that printed thousands of raw
// terminal truecolor escape sequences while building a terminal-art
// banner, flooding the chat with unreadable bracket-code text. Fixed by
// internal/execenv.StripControlSequences, called from
// internal/harness/toolout.go's sanitizeToolResult (every tool result,
// before truncation/commit) and internal/tui/transcript.go's
// summarizeLines (defense in depth for the render layer, including
// replaying a session recorded before this fix shipped).
//
// This test drives the real TUI against a faux-scripted bash call whose
// command prints truecolor half-block sequences and an OSC 52 clipboard
// write, then checks both layers this fix touches:
//   - the emulated screen never shows the raw bracket-code text (docs/
//     testing.md's screen-layer rule: assert on the glass, not the bytes)
//     and never shows the OSC 52 escape text either (the testable proxy
//     for "the clipboard was never hijacked" — a PTY test can't observe
//     the real clipboard, but it can assert the escape sequence that
//     would have written to it never reached the terminal);
//   - the committed session JSONL's stored ToolResultMessage content
//     contains no ESC byte and no leftover "[38;2;" bracket-code text —
//     the model-facing/stored-content guarantee, independent of what the
//     screen happened to render.
import (
	"os"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/session/jsonl"
)

// ansiFloodScript's bash call prints 50 truecolor half-block sequences
// (the real incident's own pattern, "\x1b[38;2;r;g;bm▀" repeated) plus an
// OSC 52 clipboard write and an OSC 0 title change, all via printf's own
// backslash-escape interpretation (the command string is YAML
// single-quoted so the backslashes reach printf literally, unprocessed by
// the enclosing shell quoting).
const ansiFloodScript = `model: faux-1
steps:
  - tool_call:
      name: bash
      args:
        command: 'i=0; while [ $i -lt 50 ]; do printf "\033[38;2;100;200;50m\xe2\x96\x80"; i=$((i+1)); done; printf "\033[0m\n"; printf "\033]52;c;aGVsbG8=\007\n"; printf "\033]0;pwned title\007\n"'
      id: b1
  - on_tool_result: b1
    then:
      - text: "Built the banner."
        usage: {input: 100, output: 20}
`

func TestTUI_ToolOutput_StripsRawAnsiEscapes(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, ansiFloodScript)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)

	s.Send("build the terminal art banner")
	s.SendKey("enter")
	waitTurnSettled(t, s)

	joined := strings.Join(append(append([]string(nil), s.Scrollback()...), s.Rows()...), "\n")

	// No raw escape byte anywhere on screen or in scrollback.
	if strings.ContainsRune(joined, '\x1b') {
		t.Errorf("screen/scrollback still contains a raw ESC byte")
	}

	// kiln legitimately echoes the bash call's own command argument back
	// as readable text (so the user can see what ran) — and that command
	// argument's literal source necessarily contains these same
	// substrings once, spelled out as "\033[38;2;...", "\033]52;c;...",
	// "\033]0;pwned title..." (backslash-digits, not a real escape byte).
	// That single echoed occurrence of each substring is expected and
	// harmless; what must never happen is a SECOND occurrence, which can
	// only come from the tool's actual OUTPUT leaking the leftover
	// bracket-code text that is left behind when only the leading ESC
	// byte is stripped and the rest of the sequence is not (confirmed by
	// reverting internal/execenv/capture.go's SanitizeShellOutput to a
	// byte-only ESC/C0 filter: the result row then showed "[38;2;100;200;
	// 50m▀[38;2;100;200;50m▀..." repeated 50 times, and "]52;c;
	// aGVsbG8=" / "]0;pwned title" as bare leftover text — exactly this
	// test's failure mode).
	const echoedOnce = 1
	if n := strings.Count(joined, "[38;2;"); n > echoedOnce {
		t.Errorf("screen/scrollback contains %d occurrences of the truecolor bracket code (want at most %d, the harmless command echo); leftover bracket-code text from the tool's output leaked through:\n%s", n, echoedOnce, joined)
	}
	if n := strings.Count(joined, "]52;c;"); n > echoedOnce {
		t.Errorf("screen/scrollback contains %d occurrences of the OSC 52 clipboard escape text (want at most %d, the harmless command echo); the clipboard-write escape leaked into the tool's rendered output:\n%s", n, echoedOnce, joined)
	}
	if n := strings.Count(joined, "]0;pwned title"); n > echoedOnce {
		t.Errorf("screen/scrollback contains %d occurrences of the OSC 0 title-change escape text (want at most %d, the harmless command echo); the title-change escape leaked into the tool's rendered output:\n%s", n, echoedOnce, joined)
	}

	// The model-facing/stored-content guarantee: open the committed
	// session JSONL directly and check the stored ToolResultMessage text
	// itself, independent of how the TUI chose to render it.
	sess := sessionFile(t, sessDir, proj)
	st, err := jsonl.Open(sess, nil)
	if err != nil {
		t.Fatalf("jsonl.Open(%s): %v", sess, err)
	}
	defer st.Close()

	var found bool
	for _, e := range st.ScanEntries(session.EntryScan{}) {
		tr, ok := e.Message.(msg.ToolResultMessage)
		// faux prefixes the script's own "id: b1" with "toolu_" when it
		// plays the call back (confirmed by reading the session file:
		// the committed ToolCall/ToolResultMessage carries
		// toolCallId "toolu_b1"), so match on ToolName instead of the
		// script's bare id.
		if !ok || tr.ToolName != "bash" {
			continue
		}
		found = true
		for _, block := range tr.Content {
			text, ok := block.(msg.TextContent)
			if !ok {
				continue
			}
			if strings.ContainsRune(text.Text, '\x1b') {
				t.Errorf("stored tool result text still contains a raw ESC byte: %q", text.Text)
			}
			if strings.Contains(text.Text, "[38;2;") {
				t.Errorf("stored tool result text still contains leftover bracket-code text: %q", text.Text)
			}
			if strings.Contains(text.Text, "]52;c;") {
				t.Errorf("stored tool result text still contains the OSC 52 escape text: %q", text.Text)
			}
		}
	}
	if !found {
		raw, _ := os.ReadFile(sess)
		t.Fatalf("no bash toolResult entry found in session file:\n%s", raw)
	}
}
