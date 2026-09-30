//go:build e2e

package e2e

// ask_user_question, driven through the real cmd/kiln binary: the model
// calls the tool, kiln shows the inline question prompt (internal/tui's
// askuser_render.go + permissionview.go's pendingQuestion), a real key
// press answers it, and the resulting "User has answered your
// questions: ..." text must reach the model's *next* request — the only
// thing that matters here, since the render states themselves are pinned
// by internal/tui's own golden tests (askuser_render_test.go). Reuses
// tui_test.go's helpers (tuiFixture, startTUI, waitReady) exactly like
// tui_behaviour_test.go's permission test does.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// jsonEscaped returns s the way it appears inside a JSON string literal
// (quotes and backslashes escaped) — requests() hands back the raw JSON
// request body, where the tool result's own literal `"` characters (the
// formatted answer text quotes each question and answer) are backslash-
// escaped, not bare.
func jsonEscaped(t *testing.T, s string) string {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Trim(string(raw), `"`)
}

// TestAskUserQuestion_AnswerReachesModel drives testdata/faux/askuser-
// behaviour.yaml (one single-select ask_user_question call, one option
// already highlighted), waits for the question prompt, presses "1" to
// pick the first option (Enter would do the same; the digit exercises
// the same path a real user reaches for first), and checks both that the
// prompt's own answer note is not needed (there is none - the tool result
// is silent, like every other tool call, until the model's own reply) and
// that the faux server's *next* request actually carried the formatted
// answer text kiln's tool result produced.
func TestAskUserQuestion_AnswerReachesModel(t *testing.T) {
	script := loadFauxScript(t, "askuser-behaviour")
	proj, home, sessDir, addr, requests := tuiFixture(t, script)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)

	s.Send("decide how to fix the bug")
	s.SendKey("enter")

	if err := s.WaitFor("Which approach should I use?", 5*time.Second); err != nil {
		t.Fatalf("never saw the question prompt: %v", err)
	}
	if err := s.WaitFor("Fast path", 3*time.Second); err != nil {
		t.Fatalf("never saw the first option: %v", err)
	}

	s.SendKey("1")

	if err := s.WaitFor("Got it, going with the fast path.", 5*time.Second); err != nil {
		t.Fatalf("never saw the model's reply after the answer: %v", err)
	}
	if err := waitQuiescent(s, 200*time.Millisecond, 3*time.Second); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(s.Rows(), "\n")
	if strings.Contains(joined, "Which approach should I use?") == false {
		t.Errorf("question text should still be visible in scrollback:\n%s", joined)
	}

	want := jsonEscaped(t, `User has answered your questions: "Which approach should I use?"="Fast path"`)
	found := false
	for _, msgs := range requests() {
		if strings.Contains(string(msgs), want) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("faux never received a request whose Messages contained the formatted answer %q", want)
	}
}

// TestAskUserQuestion_EscCancelsAndModelIsToldSoItCanProceed drives the
// same script but declines the question with Esc instead of answering it,
// checking the model gets a plain decline it can act on (matching
// exit_plan_mode's own headless/decline wording pattern) rather than the
// turn ending with nothing for the model to respond to.
func TestAskUserQuestion_EscCancelsAndModelIsToldSoItCanProceed(t *testing.T) {
	script := loadFauxScript(t, "askuser-behaviour")
	proj, home, sessDir, addr, requests := tuiFixture(t, script)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)

	s.Send("decide how to fix the bug")
	s.SendKey("enter")

	if err := s.WaitFor("Which approach should I use?", 5*time.Second); err != nil {
		t.Fatalf("never saw the question prompt: %v", err)
	}

	s.SendKey("esc")

	// The faux script's only branch is on_tool_result: q1 -> text; the
	// model always sees the tool result (declined or answered) and always
	// replies the same scripted line, so this also proves the declined
	// path completes the turn rather than hanging.
	if err := s.WaitFor("Got it, going with the fast path.", 5*time.Second); err != nil {
		t.Fatalf("turn never completed after declining the question: %v", err)
	}
	if err := waitQuiescent(s, 200*time.Millisecond, 3*time.Second); err != nil {
		t.Fatal(err)
	}

	found := false
	for _, msgs := range requests() {
		if strings.Contains(string(msgs), "declined to answer") {
			found = true
			break
		}
	}
	if !found {
		t.Error("faux never received a request whose Messages contained the decline result")
	}
}
