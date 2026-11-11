package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A PostToolUse hook's additionalContext belongs to the next assistant
// request, even when compaction builds a speculative request first.
func TestPostToolContextSurvivesCompaction(t *testing.T) {
	srv := startFaux(t, `model: faux-2
steps:
  - text: "Earlier turn."
    end_turn: true
  - tool_call: {name: read, args: {path: note.txt}, id: tc1}
    usage: {input: 31000, output: 100}
  - text: "## Goal\nKeep working."
    end_turn: true
  - tool_call: {name: bash, args: {command: "echo done"}, id: tc2}
  - on_tool_result: tc2
    then:
      - text: "Done."
`)
	proj := scratchProject(t)
	if err := os.WriteFile(filepath.Join(proj, "note.txt"), []byte(strings.Repeat("note ", 4000)), 0o644); err != nil {
		t.Fatal(err)
	}
	writeHookSettings(t, proj, "PostToolUse", "read", hooksFixture(t, "context-plaintext.sh"))

	args := baseArgs()
	args.Model = "faux/faux-2"
	args.Print = true
	args.PrintPrompt = "Earlier context"
	args.OutputFormat = "text"
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader("")); code != 0 {
		t.Fatalf("first run exit %d: %s", code, stderr.String())
	}

	args.ContinueLatest = true
	args.PrintPrompt = "Read the note. " + strings.Repeat("detail ", 3200)
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader("")); code != 0 {
		t.Fatalf("second run exit %d: %s", code, stderr.String())
	}

	reqs := srv.Requests()
	if len(reqs) != 5 {
		t.Fatalf("faux requests = %d, want first turn, tool call, compaction, two assistant continuations; stdout=%s stderr=%s", len(reqs), stdout.String(), stderr.String())
	}
	if !strings.Contains(string(reqs[2].Messages), "context summarization assistant") {
		t.Fatalf("third request was not compaction: %s", reqs[2].Messages)
	}
	const hookText = "you have 2 unread messages"
	if got := strings.Count(string(reqs[3].Messages), hookText); got != 1 {
		t.Errorf("first assistant request after compaction has hook context %d times, want once", got)
	}
	if got := strings.Count(string(reqs[4].Messages), hookText); got != 0 {
		t.Errorf("following assistant request has hook context %d times, want none", got)
	}
}

func TestPostToolContextSurvivesRequestRetry(t *testing.T) {
	srv := startFaux(t, `model: faux-1
steps:
  - tool_call: {name: read, args: {path: note.txt}, id: tc1}
  - text: "A response cut before completion."
    disconnect_after: 20
  - text: "Recovered."
`)
	proj := scratchProject(t)
	if err := os.WriteFile(filepath.Join(proj, "note.txt"), []byte("note\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeHookSettings(t, proj, "PostToolUse", "read", hooksFixture(t, "context-plaintext.sh"))

	args := baseArgs()
	args.Print = true
	args.PrintPrompt = "Read the note"
	args.OutputFormat = "text"
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader("")); code != 0 {
		t.Fatalf("run exit %d: stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	reqs := srv.Requests()
	if len(reqs) != 3 {
		t.Fatalf("faux requests = %d, want tool call, failed response, retry; stdout=%s stderr=%s", len(reqs), stdout.String(), stderr.String())
	}
	const hookText = "you have 2 unread messages"
	for i := 1; i <= 2; i++ {
		if got := strings.Count(string(reqs[i].Messages), hookText); got != 1 {
			t.Errorf("assistant attempt %d has hook context %d times, want once", i, got)
		}
	}
}
