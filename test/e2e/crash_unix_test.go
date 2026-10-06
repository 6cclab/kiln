//go:build e2e && unix

package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/testkit/screen"
)

var (
	crashtestBinOnce sync.Once
	crashtestBin     string
	crashtestBinErr  error
)

// crashtestKiln builds kiln with -tags kiln_crashtest, the build whose
// crash.TestPoint panics where KILN_CRASHTEST_PANIC says. Release builds
// and the shared harnessBin never carry it.
func crashtestKiln(t *testing.T) string {
	t.Helper()
	crashtestBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "kiln-crashtest-bin-")
		if err != nil {
			crashtestBinErr = err
			return
		}
		crashtestBin = filepath.Join(dir, "kiln")
		out, err := exec.Command("go", "build", "-tags", "kiln_crashtest", "-o", crashtestBin, "github.com/andrepato/harness/cmd/kiln").CombinedOutput()
		if err != nil {
			crashtestBinErr = err
			t.Logf("build: %s", out)
		}
	})
	if crashtestBinErr != nil {
		t.Fatalf("build the kiln_crashtest binary: %v", crashtestBinErr)
	}
	return crashtestBin
}

// TestTUI_PanicOnKilnGoroutineRestoresTerminal: a panic on a goroutine kiln
// started itself (here the provider's stream reader, the first thing a
// turn starts) used to kill the process with the terminal left in raw
// mode, mouse tracking on and the alternate screen up, and the panic text
// went to that alternate screen and was lost. Now the process writes a
// crash report next to its run logs, restores the terminal, names the
// report on stderr, and exits 2.
func TestTUI_PanicOnKilnGoroutineRestoresTerminal(t *testing.T) {
	bin := crashtestKiln(t)
	proj, home, sessDir, addr, _ := tuiFixture(t, "model: faux-1\nsteps:\n  - text: \"never streamed\"\n")

	saved := harnessBin
	harnessBin = bin
	t.Cleanup(func() { harnessBin = saved })
	record := filepath.Join(t.TempDir(), "pty.rec")
	tuiRecordPath = record
	t.Cleanup(func() { tuiRecordPath = "" })
	tuiExtraOpts = []screen.Option{
		screen.WithRecord(record),
		screen.WithEnv("KILN_CRASHTEST_PANIC", "provider-stream"),
		screen.WithUnsetEnv("HARNESS_LOG_DIR"),
		screen.WithTimeout(10 * time.Second),
	}
	t.Cleanup(func() { tuiExtraOpts = nil })

	s := startTUI(t, 120, 30, proj, home, sessDir, addr, "--fullscreen")
	waitReady(t, s)
	s.Send("hello")
	s.SendKey("enter")

	code, err := s.Exit() // err is the *exec.ExitError of a non-zero exit
	if code == -1 {
		t.Fatalf("kiln did not exit after the panic: %v", err)
	}
	if code != 2 {
		t.Errorf("exit status = %d, want 2", code)
	}
	assertTerminalRestored(t, s)
	s.Close() // flush the record file

	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	t.Logf("PTY tail: %q", tail(out, 500))

	// The terminal ends restored: for every mode kiln's program turned on,
	// the last word on it is "off".
	for _, m := range []struct{ name, on, off string }{
		{"alternate screen", "\x1b[?1049h", "\x1b[?1049l"},
		{"mouse (normal)", "\x1b[?1000h", "\x1b[?1000l"},
		{"mouse (button)", "\x1b[?1002h", "\x1b[?1002l"},
		{"mouse (any)", "\x1b[?1003h", "\x1b[?1003l"},
		{"mouse (SGR)", "\x1b[?1006h", "\x1b[?1006l"},
		{"bracketed paste", "\x1b[?2004h", "\x1b[?2004l"},
		{"hidden cursor", "\x1b[?25l", "\x1b[?25h"},
	} {
		on, off := strings.LastIndex(out, m.on), strings.LastIndex(out, m.off)
		if on >= 0 && off < on {
			t.Errorf("%s left on: last %q at %d, last %q at %d", m.name, m.on, on, m.off, off)
		}
	}
	if !strings.Contains(out, "\x1b[?1049h") {
		t.Errorf("the run never entered the alternate screen; the test is not exercising fullscreen")
	}
	if push := regexp.MustCompile(`\x1b\[>\d+u`).FindAllStringIndex(out, -1); len(push) > 0 {
		if pop := strings.LastIndex(out, "\x1b[<u"); pop < push[len(push)-1][0] {
			t.Errorf("kitty keyboard flags pushed at %d never popped", push[len(push)-1][0])
		}
	}

	// A crash report in the scratch HOME's log dir, with the panic and
	// the goroutine stacks.
	reports, _ := filepath.Glob(filepath.Join(home, ".harness", "logs", "crash-*.txt"))
	if len(reports) != 1 {
		t.Fatalf("crash reports under %s: %v, want one\nPTY tail:\n%q", home, reports, tail(out, 600))
	}
	report, err := os.ReadFile(reports[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"panic: kiln_crashtest: injected panic at provider-stream", "goroutine ", "crash.TestPoint", "all goroutines:"} {
		if !bytes.Contains(report, []byte(want)) {
			t.Errorf("crash report lacks %q:\n%.3000s", want, report)
		}
	}

	// stderr (the same PTY) names the report, after the terminal was
	// restored.
	notice := strings.LastIndex(out, "crash report: "+reports[0])
	if notice < 0 {
		t.Errorf("stderr does not name %s; PTY tail:\n%q", reports[0], tail(out, 600))
	} else if alt := strings.LastIndex(out, "\x1b[?1049l"); alt > notice {
		t.Errorf("the notice was printed before the alternate screen was left, so it was wiped")
	}
	if !strings.Contains(out, "kiln crashed: panic: kiln_crashtest: injected panic at provider-stream") {
		t.Errorf("stderr lacks the panic line; PTY tail:\n%q", tail(out, 600))
	}

	// The run log has the ERROR line naming the report.
	logs, _ := filepath.Glob(filepath.Join(home, ".harness", "logs", "harness-*.log"))
	var found bool
	for _, l := range logs {
		b, _ := os.ReadFile(l)
		if bytes.Contains(b, []byte("level=ERROR msg=panic")) && bytes.Contains(b, []byte(reports[0])) {
			found = true
		}
	}
	if !found {
		t.Errorf("no run log in %v has the panic's ERROR line naming the report", logs)
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
