//go:build e2e

package e2e

// This file covers two of the day's real-session bugs end to end:
//
//  1. A dragged/pasted image path (backslash-escaped spaces, a macOS
//     screenshot's U+202F before AM/PM) attaches as an image instead of
//     being sent as garbled text (internal/imgpath, internal/tui/app.go's
//     tea.PasteMsg handler and handleSubmit).
//  2. A slash command typed while a turn is busy never reaches the model
//     as text: a read-only one (/mcp) runs immediately, a mutating one
//     (/compact) is deferred and runs once the turn ends
//     (internal/tui/app.go's isImmediateCommand/drainDeferredCommands).

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestTUI_DraggedImagePath_FirstInPromptAttachesAsImage drives the exact
// shape of Andre's session: a dragged screenshot's path, escaped the way
// a real terminal delivers it, as the first thing in the prompt (which
// used to be misparsed as a slash command attempt, "/Users..."). It must
// attach as an image — reaching the faux model as a `"type":"image"`
// content block — and never produce an "Unknown command" error.
func TestTUI_DraggedImagePath_FirstInPromptAttachesAsImage(t *testing.T) {
	dir := t.TempDir()
	narrow := " "
	name := "Screenshot 2026-10-02 at 11.43.28" + narrow + "AM.png"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, tinyPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	escaped := strings.ReplaceAll(path, " ", `\ `)

	proj, home, sessDir, addr, requests := tuiFixture(t, "model: faux-1\nsteps:\n  - text: \"got it\"\n")
	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "bypassPermissions",
	)
	defer s.Close()
	waitReady(t, s)

	s.Send(escaped + " what is in this screenshot")
	s.SendKey("enter")

	if err := s.WaitFor("[Image #1]", 3*time.Second); err != nil {
		t.Fatalf("no [Image #1] placeholder in the transcript: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
	joined := strings.Join(s.Rows(), "\n")
	if strings.Contains(joined, "Unknown command") {
		t.Fatalf("the dragged path was treated as a slash command:\n%s", joined)
	}
	if strings.Contains(joined, "Screenshot") {
		t.Errorf("the raw escaped path leaked into the transcript, want only the placeholder:\n%s", joined)
	}

	waitTurnSettled(t, s)

	found := false
	for _, msgs := range requests() {
		if strings.Contains(string(msgs), `"type":"image"`) {
			found = true
			break
		}
	}
	if !found {
		t.Error("faux never received a request with an image content block")
	}
}

// TestTUI_ImmediateSlashCommandMidTurn_NeverReachesModel: /mcp (a
// read-only, UI-only command) typed while a turn is busy must run right
// away — its output appears before the turn ends — and its literal text
// must never reach the faux model, the actual bug from the session
// transcript ("I'm not Claude Code — I don't have a /mcp command").
func TestTUI_ImmediateSlashCommandMidTurn_NeverReachesModel(t *testing.T) {
	script := `
model: faux-1
steps:
  - text: "working on it"
    delay: 2s
`
	proj, home, sessDir, addr, requests := tuiFixture(t, script)
	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "bypassPermissions",
	)
	defer s.Close()
	waitReady(t, s)

	s.Send("first")
	s.SendKey("enter")

	deadline := time.Now().Add(3 * time.Second)
	for {
		if anyRowMatches(s, spinnerFramePattern) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("spinner never appeared after submitting the first message:\n%s", strings.Join(s.Rows(), "\n"))
		}
		time.Sleep(15 * time.Millisecond)
	}

	s.Send("/mcp")
	s.SendKey("enter")

	// Runs immediately — before the busy turn ends — rather than sitting
	// queued: the real registry's /mcp (ManageCommands, which shadows
	// InspectCommands' report-only version) opens its "Manage MCP
	// servers" panel right away.
	if err := s.WaitFor("Manage MCP servers", 3*time.Second); err != nil {
		t.Fatalf("/mcp did not run immediately while busy: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
	s.SendKey("esc")

	waitTurnSettled(t, s)
	if err := s.WaitFor("working on it", 3*time.Second); err != nil {
		t.Fatal(err)
	}

	for _, msgs := range requests() {
		if strings.Contains(string(msgs), "/mcp") {
			t.Errorf("a request's Messages contained the literal \"/mcp\" text:\n%s", msgs)
		}
	}
}

// TestTUI_DeferredSlashCommandMidTurn_RunsAfterTurnEnds: /compact (which
// mutates the conversation) typed mid-turn must not run immediately and
// must not reach the model as text either — it is held and actually run,
// as a command, once the busy turn ends.
func TestTUI_DeferredSlashCommandMidTurn_RunsAfterTurnEnds(t *testing.T) {
	script := `
model: faux-1
steps:
  - text: "working on it"
    delay: 2s
`
	proj, home, sessDir, addr, requests := tuiFixture(t, script)
	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "bypassPermissions",
	)
	defer s.Close()
	waitReady(t, s)

	s.Send("first")
	s.SendKey("enter")

	deadline := time.Now().Add(3 * time.Second)
	for {
		if anyRowMatches(s, spinnerFramePattern) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("spinner never appeared after submitting the first message:\n%s", strings.Join(s.Rows(), "\n"))
		}
		time.Sleep(15 * time.Millisecond)
	}

	s.Send("/compact")
	s.SendKey("enter")

	// Held, not run yet: with nothing in the conversation to compact,
	// /compact's own "Nothing to compact yet" (or "Context compacted")
	// note must not appear before the turn ends.
	time.Sleep(150 * time.Millisecond)
	beforeEnd := strings.Join(s.Rows(), "\n")
	if strings.Contains(beforeEnd, "Nothing to compact") || strings.Contains(beforeEnd, "Context compacted") {
		t.Fatalf("/compact ran before the turn ended:\n%s", beforeEnd)
	}
	// Held is not the same as silent: a dim "you · queued" block (the same
	// style a queued plain-text follow-up gets) must show the command is
	// waiting, not just disappear from the input until the panel/note
	// appears, unannounced, once the turn ends
	// (qa/findings/20261004T205021Z-deferred-slash-command-no-feedback.json).
	if !strings.Contains(beforeEnd, "queued") || !strings.Contains(beforeEnd, "/compact") {
		t.Fatalf("deferred /compact must show a \"queued\" block while held:\n%s", beforeEnd)
	}

	waitTurnSettled(t, s)
	if err := s.WaitFor(regexp.MustCompile(`Nothing to compact|Context compacted`), 3*time.Second); err != nil {
		t.Fatalf("/compact never ran after the turn ended: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}

	for _, msgs := range requests() {
		if strings.Contains(string(msgs), "/compact") {
			t.Errorf("a request's Messages contained the literal \"/compact\" text:\n%s", msgs)
		}
	}
}

// TestTUI_PlainSpacePastedImagePathAttaches drives the bracketed-paste
// path directly (tea.PasteMsg, not a typed/submitted line): a path with
// plain, unescaped spaces — Finder's "Copy as Pathname", which does no
// backslash escaping at all, unlike a terminal's own drag-and-drop —
// pasted as the *whole* paste content must attach as an image, same as
// the backslash-escaped form TestTUI_DraggedImagePath_FirstInPromptAttachesAsImage
// covers. Before this fix the paste stayed as plain text
// (qa/findings/20261004T205021Z-image-path-paste-unescaped-not-
// attached.json).
func TestTUI_PlainSpacePastedImagePathAttaches(t *testing.T) {
	dir := t.TempDir()
	narrow := " "
	name := "Screenshot 2026-10-04 at 3.41.07" + narrow + "PM.png"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, tinyPNG, 0o644); err != nil {
		t.Fatal(err)
	}

	proj, home, sessDir, addr, requests := tuiFixture(t, "model: faux-1\nsteps:\n  - text: \"got it\"\n")
	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "bypassPermissions",
	)
	defer s.Close()
	waitReady(t, s)

	s.Send("\x1b[200~" + path + "\x1b[201~")
	if err := s.WaitFor("[Image #1]", 3*time.Second); err != nil {
		t.Fatalf("plain-space path never attached as an image: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
	s.SendKey("enter")
	waitTurnSettled(t, s)

	found := false
	for _, msgs := range requests() {
		if strings.Contains(string(msgs), `"type":"image"`) {
			found = true
			break
		}
	}
	if !found {
		t.Error("faux never received a request with an image content block")
	}
}
