//go:build e2e

package e2e

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestTUI_ModelPickShowsSelectionAndConfirms: the selected /model row is
// marked and highlighted across its width, ←/→ changes the session's
// effort, and Enter applies the model, closes the dialog and leaves a
// confirmation in the transcript.
func TestTUI_ModelPickShowsSelectionAndConfirms(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, fixBugScript)
	s := startTUI(t, 100, 30, proj, home, sessDir, addr)
	defer s.Close()
	waitReady(t, s)
	s.Send("/model")
	s.SendKey("enter")
	if err := s.WaitFor("Select model", 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.WaitFor(regexp.MustCompile(`› 1\. faux/faux-1`), 2*time.Second); err != nil {
		t.Fatalf("the selected row has no marker: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}

	s.SendKey("right")
	// The scale starts at auto (unset); → steps to low.
	if err := s.WaitFor("Effort set to low for this session", 3*time.Second); err != nil {
		t.Fatalf("←/→ did not change the effort: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
	if err := s.WaitFor("effort  auto · low · medium · high · xhigh · max", 2*time.Second); err != nil {
		t.Errorf("the effort scale is missing: %v", err)
	}

	s.SendKey("down")
	if err := s.WaitFor(regexp.MustCompile(`› 2\. faux/faux-2`), 2*time.Second); err != nil {
		t.Fatalf("the marker did not follow the cursor: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
	rows, styles := s.Rows(), s.Styles()
	for i, r := range rows {
		if strings.Contains(r, "2. faux/faux-2") {
			col := strings.Index(r, "tier")
			if col > 0 && styles[i][len([]rune(r[:col]))].Bg == "" {
				t.Errorf("the selected row's highlight stops before its description")
			}
		}
	}

	s.SendKey("enter")
	if err := s.WaitFor(regexp.MustCompile(`Now on faux/faux-2 · small tier · [\d.]+k usable \(default for new sessions\)`), 5*time.Second); err != nil {
		t.Fatalf("no confirmation after picking: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
	waitTurnSettled(t, s)
	if n := strings.Count(strings.Join(s.Rows(), "\n"), "faux/faux-2 ·"); n != 1 {
		t.Errorf("the switch is confirmed %d times, want once:\n%s", n, strings.Join(s.Rows(), "\n"))
	}
	if strings.Contains(strings.Join(s.Rows(), "\n"), "Select model") {
		t.Errorf("the dialog stayed open after the pick:\n%s", strings.Join(s.Rows(), "\n"))
	}
}

// TestTUI_ModelSwitch_FooterUsesNewContextWindow regression-tests
// bridge.ModelSwitch dropping its "usable" (ContextWindow) argument on the
// floor: MsgModelInfo{Label: label} never carried the new tier's window,
// so after /model the footer's "ctx" percentage kept dividing by the
// previous model's window. faux-1's window is 128000, faux-2's is 32768
// (internal/provider/faux/faux.go); testdata/faux/model-switch-usage.yaml
// reports usage against each in turn, so a stale denominator and a live
// one produce different, checkable percentages.
func TestTUI_ModelSwitch_FooterUsesNewContextWindow(t *testing.T) {
	script := loadFauxScript(t, "model-switch-usage")
	proj, home, sessDir, addr, _ := tuiFixture(t, script)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)

	s.Send("go")
	s.SendKey("enter")
	waitTurnSettled(t, s)

	// 1280/128000 = 1%, against faux-1's window.
	if err := s.WaitFor("1%", 5*time.Second); err != nil {
		t.Fatalf("no 1%% context row against faux-1's 128000 window: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}

	s.Send("/model")
	s.SendKey("enter")
	if err := s.WaitFor("Select model", 3*time.Second); err != nil {
		t.Fatal(err)
	}
	s.SendKey("down")
	if err := s.WaitFor(regexp.MustCompile(`› 2\. faux/faux-2`), 2*time.Second); err != nil {
		t.Fatalf("the marker did not reach faux-2: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
	s.SendKey("enter")
	if err := s.WaitFor(regexp.MustCompile(`Now on faux/faux-2`), 5*time.Second); err != nil {
		t.Fatalf("no confirmation of the switch: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}

	s.Send("go again")
	s.SendKey("enter")
	waitTurnSettled(t, s)

	// 1638/32768 ~= 5%, against faux-2's window. A stale denominator
	// (still 128000) would show 1% again (1638/128000 rounds to 1%), so
	// this distinguishes the fix from the bug rather than just checking
	// "some percentage is shown".
	if err := s.WaitFor("5%", 5*time.Second); err != nil {
		t.Fatalf("context row did not move to faux-2's 32768 window (still showing the old model's denominator?): %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
	if strings.Contains(strings.Join(s.Rows(), "\n"), "1%") {
		t.Errorf("stale 1%% (faux-1's denominator) still present after the switch:\n%s", strings.Join(s.Rows(), "\n"))
	}
}
