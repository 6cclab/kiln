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
	if err := s.WaitFor("Effort set to high for this session", 3*time.Second); err != nil {
		t.Fatalf("←/→ did not change the effort: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
	if err := s.WaitFor("high effort", 2*time.Second); err != nil {
		t.Errorf("the effort row did not update: %v", err)
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
	if err := s.WaitFor("Model set to faux/faux-2 (default for new sessions)", 5*time.Second); err != nil {
		t.Fatalf("no confirmation after picking: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
	waitTurnSettled(t, s)
	if strings.Contains(strings.Join(s.Rows(), "\n"), "Select model") {
		t.Errorf("the dialog stayed open after the pick:\n%s", strings.Join(s.Rows(), "\n"))
	}
}
