// Package crash ends the process cleanly when a goroutine panics.
//
// An unrecovered panic in any goroutine kills the whole process on the spot.
// Bubble Tea recovers panics in its own event loop and Cmd goroutines and
// restores the terminal, but a goroutine kiln starts itself (a provider's
// stream reader, a concurrent tool, the bridge committer, an MCP client, a
// hook runner) has nobody to do that: the process dies with the terminal
// still in raw mode, mouse tracking on and the alternate screen up, and the
// panic text printed onto that alternate screen is gone the moment the
// terminal is reset by hand.
//
// Every goroutine kiln starts goes through Go (or defers Guard first). On a
// panic the handler writes a crash report (the panic value, the panicking
// goroutine's stack and every goroutine's stack) to a file next to the run
// logs and an ERROR line to the run log, restores the terminal through the
// function the interactive session registered with SetRestore, prints one
// plain line naming the report on stderr, and exits with status 2, the
// status Go itself uses for an unrecovered panic.
//
// A panic is never recovered and continued from: the state the panicking
// goroutine was changing (a session transaction, a lane's operation state,
// the transcript) may be half-written, so carrying on could corrupt the
// session on disk. The report and a usable terminal are what is saved.
package crash

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/andrepato/harness/internal/diag"
)

// ExitCode is the status a crashed process exits with: Go's own for an
// unrecovered panic.
const ExitCode = 2

// maxStackBytes bounds the all-goroutines dump.
const maxStackBytes = 64 << 20

// Handler turns a panic or a fatal signal into a crash report, a restored
// terminal and an exit. Its fields are seams for tests; the process uses
// Default.
type Handler struct {
	// Dir is where crash reports are written: the run log's directory,
	// or "" when this process has no run log (tests, subcommands), and
	// then the stack goes to stderr only.
	Dir func() string
	// Logger is the run log.
	Logger func() *slog.Logger
	// LogPath names the run log in the report ("" when there is none).
	LogPath func() string
	// Stderr receives the one-line notice.
	Stderr io.Writer
	// Exit ends the process. It does not return in production.
	Exit func(code int)
	// Now stamps the report and its file name.
	Now func() time.Time

	restoreMu sync.Mutex
	restore   func()

	// ending is locked by the first crash and by Exit, and never
	// unlocked: whichever gets it ends the process. A second goroutine
	// that panics while the first crash is being reported waits here
	// rather than racing it to exit, and a normal exit path (main's
	// os.Exit after Bubble Tea returns because the crash killed it) waits
	// for the report and the crash's exit status.
	ending sync.Mutex

	recordMu sync.Mutex
	recorded string

	// crashing is set once Panic or Signal starts.
	crashing atomic.Bool
}

// Default is the process's handler.
var Default = &Handler{
	Dir:     runLogDir,
	Logger:  diag.L,
	LogPath: diag.Path,
	Stderr:  os.Stderr,
	Exit:    os.Exit,
	Now:     time.Now,
}

// runLogDir is the directory of this run's log, "" before diag.Start.
func runLogDir() string {
	if p := diag.Path(); p != "" {
		return filepath.Dir(p)
	}
	return ""
}

// Go runs fn on a new goroutine guarded by Default.
//
// fn's own deferred calls run before the guard, while the panic unwinds.
// Do not defer what releases another goroutine (closing a channel it waits
// on, wg.Done): it would carry on with the panicking work's half-made
// results until the process exits. Do it after the work instead.
func Go(fn func()) {
	go func() {
		defer Guard()
		fn()
	}()
}

// Guard, deferred first thing in a goroutine, turns a panic in that
// goroutine into Default's crash report, terminal restore and exit.
func Guard() {
	if r := recover(); r != nil {
		Default.Panic(r, debug.Stack())
	}
}

// Guard is the package Guard for this handler.
func (h *Handler) Guard() {
	if r := recover(); r != nil {
		h.Panic(r, debug.Stack())
	}
}

// SetRestore registers fn as the way to give the terminal back (raw mode
// off, alternate screen and mouse reporting off, cursor shown). The
// returned func unregisters it, when the terminal no longer needs it.
func SetRestore(fn func()) (unset func()) { return Default.SetRestore(fn) }

// SetRestore is the package SetRestore for this handler.
func (h *Handler) SetRestore(fn func()) (unset func()) {
	h.restoreMu.Lock()
	h.restore = fn
	h.restoreMu.Unlock()
	return func() {
		h.restoreMu.Lock()
		h.restore = nil
		h.restoreMu.Unlock()
	}
}

// Exit ends the process with code, unless a crash is being reported, in
// which case it waits for that crash to end the process instead. Use it
// for the process's normal exit so a crash in another goroutine is never
// cut short by it.
func Exit(code int) { Default.ExitNormally(code) }

// ExitNormally is the package Exit for this handler.
func (h *Handler) ExitNormally(code int) {
	h.ending.Lock()
	h.Exit(code)
}

// Panic reports a recovered panic value r, stack being the panicking
// goroutine's own stack (debug.Stack from the deferred recover), restores
// the terminal and exits with ExitCode.
func (h *Handler) Panic(r any, stack []byte) {
	h.crashing.Store(true)
	h.ending.Lock() // never unlocked: see ending
	path := h.write("panic: "+panicText(r), stack)
	h.Logger().Error("panic", "panic", panicText(r), "crash_report", path, "stack", string(stack))
	h.runRestore()
	h.notice("kiln crashed: panic: "+firstLine(panicText(r)), path, stack)
	h.Exit(ExitCode)
}

// Signal reports a fatal signal the way a panic is reported (every
// goroutine's stack, which is what Go's own SIGQUIT dump would have
// printed onto the terminal), restores the terminal and exits with code.
func (h *Handler) Signal(sig os.Signal, code int) {
	h.crashing.Store(true)
	h.ending.Lock() // never unlocked: see ending
	path := h.write("signal: "+sig.String(), nil)
	h.Logger().Error("fatal signal", "signal", sig.String(), "crash_report", path)
	h.runRestore()
	h.notice("kiln stopped by signal: "+sig.String(), path, nil)
	h.Exit(code)
}

// Record writes a crash report for a panic someone else recovered (Bubble
// Tea, which restores the terminal itself) and returns the report's path,
// "" when it could not be written. It does not exit.
func Record(r any, stack []byte) string { return Default.Record(r, stack) }

// Record is the package Record for this handler.
func (h *Handler) Record(r any, stack []byte) string {
	if h.crashing.Load() {
		// A panic on a guarded goroutine is already ending the process,
		// and this one is most likely its consequence: leave the report
		// and the exit to it.
		select {}
	}
	path := h.write("panic: "+panicText(r), stack)
	h.Logger().Error("panic", "panic", panicText(r), "crash_report", path, "stack", string(stack), "recovered_by", "bubbletea")
	h.recordMu.Lock()
	if h.recorded == "" {
		h.recorded = path
	}
	h.recordMu.Unlock()
	return path
}

// Recorded is the report Record wrote first, "" when it never ran.
func Recorded() string { return Default.Recorded() }

// Recorded is the package Recorded for this handler.
func (h *Handler) Recorded() string {
	h.recordMu.Lock()
	defer h.recordMu.Unlock()
	return h.recorded
}

func (h *Handler) runRestore() {
	h.restoreMu.Lock()
	fn := h.restore
	h.restoreMu.Unlock()
	if fn == nil {
		return
	}
	// A restore that itself panics must not stop the report and the
	// exit; the terminal is then left as it is.
	defer func() { _ = recover() }()
	fn()
}

// notice prints the plain stderr line. When no report could be written,
// the stack is printed instead, so it is not lost.
func (h *Handler) notice(head, path string, stack []byte) {
	if path != "" {
		fmt.Fprintf(h.Stderr, "\n%s\ncrash report: %s\n", head, path)
		return
	}
	fmt.Fprintf(h.Stderr, "\n%s\n(no crash report written)\n%s\n", head, stack)
}

// write saves the report and returns its path, "" on failure.
func (h *Handler) write(what string, stack []byte) string {
	now := h.Now()
	var b bytes.Buffer
	fmt.Fprintf(&b, "kiln crash report\n")
	fmt.Fprintf(&b, "time: %s\n", now.Format(time.RFC3339Nano))
	fmt.Fprintf(&b, "pid: %d\n", os.Getpid())
	fmt.Fprintf(&b, "args: %s\n", strings.Join(os.Args, " "))
	fmt.Fprintf(&b, "go: %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	if bi, ok := debug.ReadBuildInfo(); ok {
		fmt.Fprintf(&b, "module: %s %s\n", bi.Main.Path, bi.Main.Version)
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" || s.Key == "vcs.modified" || s.Key == "-tags" {
				fmt.Fprintf(&b, "%s: %s\n", s.Key, s.Value)
			}
		}
	}
	if h.LogPath != nil {
		if p := h.LogPath(); p != "" {
			fmt.Fprintf(&b, "run log: %s\n", p)
		}
	}
	fmt.Fprintf(&b, "\n%s\n", what)
	if len(stack) > 0 {
		fmt.Fprintf(&b, "\n%s", stack)
	}
	fmt.Fprintf(&b, "\nall goroutines:\n\n%s", allStacks())

	dir := h.Dir()
	if dir == "" {
		return ""
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	// Exclusive create: two reports in the same second (a Bubble Tea
	// panic recorded beside a guarded one) must not overwrite each other.
	base := fmt.Sprintf("crash-%s-%d", now.Format("20060102-150405"), os.Getpid())
	for i := 1; i < 100; i++ {
		name := base + ".txt"
		if i > 1 {
			name = fmt.Sprintf("%s-%d.txt", base, i)
		}
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return ""
		}
		_, werr := f.Write(b.Bytes())
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return ""
		}
		return path
	}
	return ""
}

// allStacks is runtime.Stack for every goroutine, grown until it fits.
func allStacks() []byte {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) || len(buf) >= maxStackBytes {
			return buf[:n]
		}
		buf = make([]byte, 2*len(buf))
	}
}

func panicText(r any) string {
	if err, ok := r.(error); ok {
		return err.Error()
	}
	return fmt.Sprint(r)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}
