//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestTUI_RewindEnterApplies: Esc Esc on an empty prompt opens Rewind;
// choosing an earlier turn and pressing Enter rewinds to before it. In a
// real terminal Enter left the dialog open with nothing applied.
func TestTUI_RewindEnterApplies(t *testing.T) {
	script := `
model: faux-1
steps:
  - text: "First turn reply."
    end_turn: true
  - text: "Second turn reply."
`
	proj, home, sessDir, addr, _ := tuiFixture(t, script)
	s := startTUI(t, 120, 40, proj, home, sessDir, addr, "--permission-mode", "dontAsk")
	waitReady(t, s)
	for _, p := range []struct{ in, out string }{{"turn one", "First turn reply."}, {"turn two", "Second turn reply."}} {
		s.Send(p.in)
		s.SendKey("enter")
		if err := s.WaitFor(p.out, 5*time.Second); err != nil {
			t.Fatalf("%v\n%s", err, strings.Join(s.Rows(), "\n"))
		}
		waitTurnSettled(t, s)
	}
	s.SendKey("esc")
	s.SendKey("esc")
	if err := s.WaitFor("Files are not restored", 3*time.Second); err != nil {
		t.Fatalf("rewind dialog did not open:\n%s", strings.Join(s.Rows(), "\n"))
	}
	// Selection is colour-only (no marker glyph), so there is no text to
	// wait on between the two keys; give the dialog time to take "up".
	time.Sleep(500 * time.Millisecond)
	s.SendKey("up")
	time.Sleep(500 * time.Millisecond)
	s.SendKey("enter")
	if err := s.WaitFor("Rewound to before: turn two", 3*time.Second); err != nil {
		t.Fatalf("enter did not apply the rewind:\n%s", strings.Join(s.Rows(), "\n"))
	}
}

// TestTUI_RewindCommandOpensPicker: typing /rewind opens the same picker
// as esc esc, rather than printing a list of ids to copy.
func TestTUI_RewindCommandOpensPicker(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, `
model: faux-1
steps:
  - text: "First turn reply."
`)
	s := startTUI(t, 120, 40, proj, home, sessDir, addr, "--permission-mode", "dontAsk")
	waitReady(t, s)
	s.Send("turn one")
	s.SendKey("enter")
	if err := s.WaitFor("First turn reply.", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	waitTurnSettled(t, s)
	s.Send("/rewind")
	s.SendKey("enter")
	if err := s.WaitFor("Files are not restored", 3*time.Second); err != nil {
		t.Fatalf("/rewind did not open the picker:\n%s", strings.Join(s.Rows(), "\n"))
	}
}

// TestRewind_PrintModeShortIDPrefix: /rewind lists 8-character ids, takes
// any unique prefix, and goes back to before that message, the same as
// the picker.
func TestRewind_PrintModeShortIDPrefix(t *testing.T) {
	addr, _ := startFaux(t, `model: faux-1
steps:
  - text: "one"
  - text: "two"
`)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	env := baseEnv(home, sessDir, addr)
	for _, args := range [][]string{{"-p", "turn one"}, {"-c", "-p", "turn two"}} {
		if res := runHarness(t, proj, env, append(args, "--output-format", "text")...); res.Code != 0 {
			t.Fatalf("%v: exit %d, stderr=%s", args, res.Code, res.Stderr)
		}
	}
	list := runHarness(t, proj, env, "-c", "-p", "/rewind")
	var id string
	for _, line := range strings.Split(list.Stdout, "\n") {
		if f := strings.Fields(line); len(f) >= 3 && f[1] == "turn" && f[2] == "two" {
			id = f[0]
		}
	}
	if len(id) != 8 {
		t.Fatalf("no 8-character id listed for \"turn two\":\n%s", list.Stdout)
	}
	res := runHarness(t, proj, env, "-c", "-p", "/rewind "+id[:5])
	if !strings.Contains(res.Stdout, "Rewound to before: turn two") {
		t.Fatalf("/rewind %s: %s %s", id[:5], res.Stdout, res.Stderr)
	}
	after := runHarness(t, proj, env, "-c", "-p", "/rewind")
	if strings.Contains(after.Stdout, "turn two") || !strings.Contains(after.Stdout, "turn one") {
		t.Errorf("after rewinding, the list should hold only turn one:\n%s", after.Stdout)
	}
}
