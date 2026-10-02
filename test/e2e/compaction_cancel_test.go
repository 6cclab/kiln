//go:build e2e

package e2e

// Compaction that never finishes. A /compact sent to a model that never
// answers used to run on the TUI's Update goroutine: the whole event loop
// sat inside the HTTP request, so Esc, Ctrl+C and even SIGTERM (bubbletea
// turns it into a message for that same loop) did nothing and only SIGKILL
// ended the process. These tests drive the real binary against a faux
// model whose compaction step never answers.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/testkit/screen"
)

// longReply is ~12k tokens by kiln's chars/4 estimate: four of them
// outgrow faux-1's 32k retained tail, so /compact has history to summarise.
var longReply = strings.Repeat("The quick brown fox jumps over the lazy dog. ", 1100)

// hangingCompactionScript scripts seedTurns long replies, then a
// compaction answer that never arrives, then after (one reply each).
func hangingCompactionScript(seedTurns int, after ...string) string {
	var b strings.Builder
	b.WriteString("model: faux-1\nsteps:\n")
	for i := 0; i < seedTurns; i++ {
		b.WriteString("  - text: \"" + longReply + "\"\n    end_turn: true\n")
	}
	b.WriteString("  - text: \"never sent\"\n    delay: 10m\n    end_turn: true\n")
	for _, a := range after {
		b.WriteString("  - text: \"" + a + "\"\n    end_turn: true\n")
	}
	return b.String()
}

// seedConversation runs turns print-mode turns into proj's session, so a
// TUI started with -c continues a conversation long enough to compact.
func seedConversation(t *testing.T, proj, home, sessDir, addr string, turns int) {
	t.Helper()
	env := baseEnv(home, sessDir, addr)
	for i := 0; i < turns; i++ {
		args := []string{"-p", "tell me more", "--output-format", "text"}
		if i > 0 {
			args = append([]string{"-c"}, args...)
		}
		if res := runHarness(t, proj, env, args...); res.Code != 0 {
			t.Fatalf("seed turn %d: exit %d, stderr=%s", i+1, res.Code, res.Stderr)
		}
	}
}

// recordTUI makes the next startTUI tee the PTY's raw output to a file, so
// a goroutine dump (SIGQUIT writes it to stderr, the PTY) can be read back.
func recordTUI(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pty.rec")
	tuiExtraOpts = []screen.Option{screen.WithRecord(path), screen.WithEnv("GOTRACEBACK", "all")}
	t.Cleanup(func() { tuiExtraOpts = nil })
	return path
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;?<>=]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[@-Z\\-_]`)

// dumpGoroutines asks a stuck process for its goroutine dump (SIGQUIT),
// logs it, and copies it to $KILN_GOROUTINE_DUMP when that is set.
func dumpGoroutines(t *testing.T, s *screen.Screen, recordPath string) {
	t.Helper()
	if s.Exited() {
		return
	}
	_ = s.Signal(syscall.SIGQUIT)
	_, _ = s.WaitExit(5 * time.Second)
	raw, err := os.ReadFile(recordPath)
	if err != nil {
		t.Logf("no goroutine dump: %v", err)
		return
	}
	text := ansiPattern.ReplaceAllString(string(raw), "")
	if i := strings.Index(text, "SIGQUIT"); i >= 0 {
		text = text[i:]
	}
	if out := os.Getenv("KILN_GOROUTINE_DUMP"); out != "" {
		_ = os.WriteFile(out, []byte(text), 0o644)
	}
	t.Logf("goroutine dump of the stuck process:\n%s", text)
}

// startHangingCompaction seeds a conversation, starts the TUI on it, sends
// /compact, and waits until the faux model has received the compaction
// request it will never answer.
func startHangingCompaction(t *testing.T, args []string, after ...string) (s *screen.Screen, recordPath, proj, sessDir string, requests func() []recordedMessages) {
	t.Helper()
	const seedTurns = 4
	proj, home, sessDir, addr, requests := tuiFixture(t, hangingCompactionScript(seedTurns, after...))
	seedConversation(t, proj, home, sessDir, addr, seedTurns)
	recordPath = recordTUI(t)
	s = startTUI(t, 100, 30, proj, home, sessDir, addr, append([]string{"-c"}, args...)...)
	waitReady(t, s)
	submitSlashCommand(s, "compact")
	deadline := time.Now().Add(10 * time.Second)
	for len(requests()) < seedTurns+1 {
		if time.Now().After(deadline) {
			t.Fatalf("the compaction request never reached the model (%d requests):\n%s", len(requests()), strings.Join(s.Rows(), "\n"))
		}
		time.Sleep(20 * time.Millisecond)
	}
	return s, recordPath, proj, sessDir, requests
}

// assertTerminalRestored checks the program left the terminal as it found
// it: out of the alternate screen and out of raw mode.
func assertTerminalRestored(t *testing.T, s *screen.Screen) {
	t.Helper()
	if s.AltScreen() {
		t.Error("terminal left on the alternate screen")
	}
	raw, err := s.RawMode()
	if err != nil {
		t.Errorf("read termios: %v", err)
	} else if raw {
		t.Error("terminal left in raw mode")
	}
}

// TestTUI_Compact_EscCancels: while /compact waits on a model that never
// answers, the busy line says so, typing still reaches the input, Esc
// cancels, and the session is exactly as it was — the next prompt is sent
// with the whole, uncompacted conversation.
func TestTUI_Compact_EscCancels(t *testing.T) {
	s, rec, proj, sessDir, requests := startHangingCompaction(t, nil, "Still here.")
	if err := s.WaitFor("Compacting conversation", 3*time.Second); err != nil {
		dumpGoroutines(t, s, rec)
		t.Fatalf("no compaction status while it runs: %v", err)
	}
	s.Send("still typing")
	if err := s.WaitFor("still typing", 3*time.Second); err != nil {
		dumpGoroutines(t, s, rec)
		t.Fatalf("typing during compaction never reached the input: %v", err)
	}
	s.SendKey("esc")
	if err := s.WaitFor("Compaction cancelled", 3*time.Second); err != nil {
		dumpGoroutines(t, s, rec)
		t.Fatalf("Esc did not cancel compaction: %v", err)
	}
	if anyRowMatches(s, spinnerFramePattern) {
		t.Errorf("busy line still up after cancelling:\n%s", strings.Join(s.Rows(), "\n"))
	}
	if data, err := os.ReadFile(sessionFile(t, sessDir, proj)); err != nil {
		t.Fatal(err)
	} else if strings.Contains(string(data), `"type":"compaction"`) {
		t.Error("a cancelled compaction still wrote a compaction entry")
	}

	// The typed text is still in the input; send it.
	s.SendKey("enter")
	if err := s.WaitFor("Still here.", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	reqs := requests()
	last := string(reqs[len(reqs)-1])
	if n := strings.Count(last, "The quick brown fox"); n < 4*1000 {
		t.Errorf("the turn after a cancelled compaction carried %d copies of the seeded text, want the whole conversation (>= 4000)", n)
	}
	if strings.Contains(last, "<summary>") {
		t.Error("the turn after a cancelled compaction was sent a summary")
	}
}

// TestTUI_Compact_CtrlCTwiceExits: Ctrl+C twice during a compaction that
// never answers exits the program and restores the terminal.
func TestTUI_Compact_CtrlCTwiceExits(t *testing.T) {
	s, rec, _, _, _ := startHangingCompaction(t, []string{"--fullscreen"})
	s.SendKey("ctrl+c")
	if err := s.WaitFor("Press Ctrl-C again to exit", 3*time.Second); err != nil {
		dumpGoroutines(t, s, rec)
		t.Fatalf("first Ctrl+C had no effect: %v", err)
	}
	s.SendKey("ctrl+c")
	if _, err := s.WaitExit(3 * time.Second); err != nil {
		dumpGoroutines(t, s, rec)
		t.Fatalf("second Ctrl+C did not exit: %v", err)
	}
	assertTerminalRestored(t, s)
}

// TestTUI_Signals_ExitPromptly: SIGTERM and SIGHUP end the program within
// two seconds and restore the terminal whatever it is doing: idle, in a
// turn whose model never answers, or in a compaction that never answers.
func TestTUI_Signals_ExitPromptly(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		for _, phase := range []string{"idle", "turn", "compaction"} {
			t.Run(sig.String()+"/"+phase, func(t *testing.T) {
				var s *screen.Screen
				var rec string
				switch phase {
				case "compaction":
					s, rec, _, _, _ = startHangingCompaction(t, []string{"--fullscreen"})
				default:
					proj, home, sessDir, addr, requests := tuiFixture(t, hangingCompactionScript(0))
					rec = recordTUI(t)
					s = startTUI(t, 100, 30, proj, home, sessDir, addr, "--fullscreen")
					waitReady(t, s)
					if phase == "turn" {
						s.Send("hello")
						s.SendKey("enter")
						deadline := time.Now().Add(5 * time.Second)
						for len(requests()) < 1 {
							if time.Now().After(deadline) {
								t.Fatal("the turn's request never reached the model")
							}
							time.Sleep(20 * time.Millisecond)
						}
					}
				}
				if !s.AltScreen() {
					t.Fatal("precondition: --fullscreen should be on the alternate screen")
				}
				if err := s.Signal(sig); err != nil {
					t.Fatal(err)
				}
				if _, err := s.WaitExit(2 * time.Second); err != nil {
					dumpGoroutines(t, s, rec)
					t.Fatalf("%s did not end the program: %v", sig, err)
				}
				assertTerminalRestored(t, s)
			})
		}
	}
}

// TestTUI_Turn_NeverAnswers_EscInterrupts: the same never-answering model
// on an ordinary turn. Turns already run off the Update goroutine; this
// pins that Esc still interrupts one.
func TestTUI_Turn_NeverAnswers_EscInterrupts(t *testing.T) {
	proj, home, sessDir, addr, requests := tuiFixture(t, hangingCompactionScript(0))
	rec := recordTUI(t)
	s := startTUI(t, 100, 30, proj, home, sessDir, addr)
	waitReady(t, s)
	s.Send("hello")
	s.SendKey("enter")
	deadline := time.Now().Add(5 * time.Second)
	for len(requests()) < 1 {
		if time.Now().After(deadline) {
			t.Fatal("the turn's request never reached the model")
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.SendKey("esc")
	if err := s.WaitFor("Interrupted", 3*time.Second); err != nil {
		dumpGoroutines(t, s, rec)
		t.Fatalf("Esc did not interrupt a turn whose model never answers: %v", err)
	}
}
