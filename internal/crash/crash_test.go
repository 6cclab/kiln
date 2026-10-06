package crash

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type fakeEnv struct {
	h        *Handler
	dir      string
	log      *bytes.Buffer
	stderr   *bytes.Buffer
	exits    chan int
	restores int
	mu       sync.Mutex
}

func newFakeEnv(t *testing.T) *fakeEnv {
	t.Helper()
	e := &fakeEnv{
		dir:    t.TempDir(),
		log:    &bytes.Buffer{},
		stderr: &bytes.Buffer{},
		exits:  make(chan int, 4),
	}
	logger := slog.New(slog.NewTextHandler(e.log, nil))
	e.h = &Handler{
		Dir:     func() string { return e.dir },
		Logger:  func() *slog.Logger { return logger },
		LogPath: func() string { return "/scratch/harness-run.log" },
		Stderr:  e.stderr,
		Exit:    func(code int) { e.exits <- code },
		Now:     func() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC) },
	}
	e.h.SetRestore(func() {
		e.mu.Lock()
		e.restores++
		e.mu.Unlock()
	})
	return e
}

func (e *fakeEnv) report(t *testing.T) (string, string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(e.dir, "crash-*.txt"))
	if err != nil || len(files) != 1 {
		t.Fatalf("crash reports in %s: %v (err %v), want exactly one", e.dir, files, err)
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	return files[0], string(b)
}

//go:noinline
func nilMap() map[string]int { return nil }

//go:noinline
func panickingWorker() {
	m := nilMap()
	m["boom"] = 1 // assignment to entry in nil map
}

// A panic in a guarded goroutine writes a report with the panic value,
// the panicking goroutine's stack and every goroutine's stack, logs it,
// restores the terminal, names the report on stderr and exits 2.
func TestGuardReportsRestoresAndExits(t *testing.T) {
	e := newFakeEnv(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer e.h.Guard()
		panickingWorker()
	}()
	select {
	case code := <-e.exits:
		if code != ExitCode {
			t.Fatalf("exit code = %d, want %d", code, ExitCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler never exited")
	}
	<-done

	path, text := e.report(t)
	if want := "crash-20261006-120000-"; !strings.HasPrefix(filepath.Base(path), want) {
		t.Errorf("report name %q, want prefix %q", filepath.Base(path), want)
	}
	for _, want := range []string{
		"panic: assignment to entry in nil map",
		"goroutine ",
		"crash.panickingWorker", // the panic site, from the panicking goroutine's stack
		"all goroutines:",
		"run log: /scratch/harness-run.log",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
	if e.restores != 1 {
		t.Errorf("restore ran %d times, want 1", e.restores)
	}
	if got := e.stderr.String(); !strings.Contains(got, "kiln crashed: panic: assignment to entry in nil map") || !strings.Contains(got, "crash report: "+path) {
		t.Errorf("stderr = %q, want the panic and the report path", got)
	}
	if got := e.log.String(); !strings.Contains(got, "level=ERROR") || !strings.Contains(got, "crash_report="+path) || !strings.Contains(got, "panickingWorker") {
		t.Errorf("run log = %q, want an ERROR line naming the report with the stack", got)
	}
}

// With no report directory writable, the stack goes to stderr instead.
func TestGuardWithoutReportPrintsStack(t *testing.T) {
	e := newFakeEnv(t)
	blocker := filepath.Join(e.dir, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	e.h.Dir = func() string { return filepath.Join(blocker, "logs") }
	go func() {
		defer e.h.Guard()
		panickingWorker()
	}()
	<-e.exits
	if got := e.stderr.String(); !strings.Contains(got, "no crash report written") || !strings.Contains(got, "panickingWorker") {
		t.Errorf("stderr = %q, want the stack when no report could be written", got)
	}
}

// A restore that panics does not stop the exit.
func TestGuardSurvivesPanickingRestore(t *testing.T) {
	e := newFakeEnv(t)
	e.h.SetRestore(func() { panic("restore broke") })
	go func() {
		defer e.h.Guard()
		panic("first")
	}()
	select {
	case <-e.exits:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never exited")
	}
}

// A second panic while the first is being reported waits for the first
// crash to end the process; only one report and one exit happen.
func TestSecondPanicWaitsForFirst(t *testing.T) {
	e := newFakeEnv(t)
	release := make(chan struct{})
	e.h.SetRestore(func() { <-release })
	go func() {
		defer e.h.Guard()
		panic("first")
	}()
	time.Sleep(50 * time.Millisecond)
	go func() {
		defer e.h.Guard()
		panic("second")
	}()
	time.Sleep(50 * time.Millisecond)
	close(release)
	select {
	case <-e.exits:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never exited")
	}
	select {
	case <-e.exits:
		t.Fatal("a second exit ran")
	case <-time.After(200 * time.Millisecond):
	}
	_, text := e.report(t)
	if !strings.Contains(text, "panic: first") {
		t.Errorf("report is not the first panic's:\n%s", text)
	}
}

// A normal exit waits while a crash is being reported.
func TestExitNormallyWaitsForCrash(t *testing.T) {
	e := newFakeEnv(t)
	release := make(chan struct{})
	e.h.SetRestore(func() { <-release })
	go func() {
		defer e.h.Guard()
		panic("first")
	}()
	time.Sleep(50 * time.Millisecond)
	go e.h.ExitNormally(0)
	time.Sleep(50 * time.Millisecond)
	close(release)
	if code := <-e.exits; code != ExitCode {
		t.Fatalf("first exit = %d, want the crash's %d", code, ExitCode)
	}
}

func TestSignalWritesAllGoroutines(t *testing.T) {
	e := newFakeEnv(t)
	e.h.Signal(syscall.SIGQUIT, 131)
	if code := <-e.exits; code != 131 {
		t.Fatalf("exit = %d, want 131", code)
	}
	_, text := e.report(t)
	if !strings.Contains(text, "signal: quit") || !strings.Contains(text, "all goroutines:") || !strings.Contains(text, "TestSignalWritesAllGoroutines") {
		t.Errorf("report:\n%s", text)
	}
	if e.restores != 1 {
		t.Errorf("restore ran %d times, want 1", e.restores)
	}
}

func TestRecordDoesNotExit(t *testing.T) {
	e := newFakeEnv(t)
	path := e.h.Record("view broke", []byte("goroutine 1 [running]:\nmain.view()\n"))
	if path == "" || e.h.Recorded() != path {
		t.Fatalf("Record = %q, Recorded = %q", path, e.h.Recorded())
	}
	select {
	case <-e.exits:
		t.Fatal("Record exited")
	default:
	}
	if e.restores != 0 {
		t.Error("Record restored the terminal; Bubble Tea does that itself")
	}
	_, text := e.report(t)
	if !strings.Contains(text, "panic: view broke") || !strings.Contains(text, "main.view()") {
		t.Errorf("report:\n%s", text)
	}
}

func TestUnsetRestore(t *testing.T) {
	e := newFakeEnv(t)
	e.h.SetRestore(func() { t.Error("unset restore ran") })()
	go func() {
		defer e.h.Guard()
		panic("x")
	}()
	<-e.exits
}

// Two reports in the same second do not overwrite each other.
func TestReportsInOneSecondKeepBoth(t *testing.T) {
	e := newFakeEnv(t)
	a := e.h.Record("one", nil)
	b := e.h.Record("two", nil)
	if a == "" || b == "" || a == b {
		t.Fatalf("Record paths %q, %q: want two distinct reports", a, b)
	}
	for p, want := range map[string]string{a: "panic: one", b: "panic: two"} {
		if data, err := os.ReadFile(p); err != nil || !strings.Contains(string(data), want) {
			t.Errorf("%s: %v, want %q", p, err, want)
		}
	}
}

// A panic Bubble Tea catches while a guarded crash is ending the process
// (typically its consequence) leaves the report and the exit to that
// crash: it neither writes a second report nor returns.
func TestRecordDuringCrashLeavesItToTheCrash(t *testing.T) {
	e := newFakeEnv(t)
	release := make(chan struct{})
	e.h.SetRestore(func() { <-release })
	go func() {
		defer e.h.Guard()
		panic("first")
	}()
	time.Sleep(50 * time.Millisecond)
	returned := make(chan struct{})
	go func() {
		e.h.Record("consequence", nil)
		close(returned)
	}()
	select {
	case <-returned:
		t.Fatal("Record returned during a crash")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-e.exits
	_, text := e.report(t) // exactly one report
	if !strings.Contains(text, "panic: first") {
		t.Errorf("report:\n%s", text)
	}
}

// Without a run log (tests, subcommands) nothing is written to disk.
func TestNoRunLogNoReport(t *testing.T) {
	e := newFakeEnv(t)
	e.h.Dir = func() string { return "" }
	go func() {
		defer e.h.Guard()
		panickingWorker()
	}()
	<-e.exits
	if files, _ := filepath.Glob(filepath.Join(e.dir, "*")); len(files) != 0 {
		t.Errorf("files written: %v", files)
	}
	if !strings.Contains(e.stderr.String(), "panickingWorker") {
		t.Errorf("stderr lacks the stack: %q", e.stderr.String())
	}
}
