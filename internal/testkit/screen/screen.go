// Package screen drives a real terminal program through a real
// pseudo-terminal and a real VT emulator, so tests can ask "what is on
// screen" instead of asserting on the bytes a program wrote.
//
// This mirrors test/support/screen.ts in the TypeScript project (see
// docs/testing.md there): a write-log has no cursor, no scroll region and no
// cell grid, so it cannot tell a panel that blanks its rows apart from a
// panel that gives them back. Both produce plausible-looking, different byte
// streams for the same bug. A cell grid can.
//
// Unlike the TypeScript Screen (which runs in-process against
// @xterm/headless because node-pty does not build in that environment), this
// package spawns the real compiled binary in a real PTY and feeds its
// output into github.com/charmbracelet/x/vt, the VT emulator behind
// github.com/charmbracelet/x/vttest. That is possible here because
// github.com/creack/pty builds fine in this environment.
//
// We build directly on github.com/creack/pty and github.com/charmbracelet/x/vt
// rather than vttest.NewTerminal. vttest.NewTerminal wires its two forwarding
// goroutines (PTY output -> emulator, encoded keys -> PTY) internally with no
// interception point, and WithRecord needs to tee the raw byte stream as it
// arrives. Using the same underlying pieces vttest itself uses (a PTY and a
// vt.SafeEmulator) gets the identical VT behavior with a seam for recording.
package screen

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

// DefaultTimeout is used by WaitFor and Golden when no explicit timeout is
// given, and as the process-exit wait bound for Exit/Close.
const DefaultTimeout = 5 * time.Second

// config accumulates options before a Screen is started.
type config struct {
	env        map[string]string
	unsetEnv   map[string]bool
	timeout    time.Duration
	recordPath string
}

// Option configures a Screen at Start/StartDetached time.
type Option func(*config)

// WithEnv sets or overrides an environment variable for the spawned process.
// It may be called multiple times; later calls win. Passing "KEY=" (empty
// value) sets the variable to empty; to remove a variable entirely from the
// forced defaults use WithUnsetEnv.
func WithEnv(key, value string) Option {
	return func(c *config) {
		delete(c.unsetEnv, key)
		c.env[key] = value
	}
}

// WithUnsetEnv removes a variable from the environment passed to the
// process, including one of the forced defaults (TERM, COLORTERM, HOME).
func WithUnsetEnv(key string) Option {
	return func(c *config) {
		delete(c.env, key)
		c.unsetEnv[key] = true
	}
}

// WithTimeout sets the default timeout used by WaitFor and Golden when they
// are not given one explicitly, and the bound Exit/Close wait for process
// exit.
func WithTimeout(d time.Duration) Option {
	return func(c *config) { c.timeout = d }
}

// WithRecord tees the raw bytes the process writes to the PTY, and the raw
// bytes sent to it, to the file at path. Input is marked with a "\x00IN:"
// prefix before each write, mirroring HARNESS_RECORD_TTY in the TypeScript
// project (see docs/testing.md there).
func WithRecord(path string) Option {
	return func(c *config) { c.recordPath = path }
}

func newConfig(opts []Option) *config {
	c := &config{
		env:      map[string]string{},
		unsetEnv: map[string]bool{},
		timeout:  DefaultTimeout,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Screen drives a spawned process through a PTY and a VT emulator.
type Screen struct {
	tb   testing.TB // nil when started via StartDetached
	cols int
	rows int

	cmd  *exec.Cmd
	ptmx *os.File

	emu *vt.SafeEmulator
	// emuMu serializes every access that reads a *uv.Cell (or other pointer)
	// out of emu against the goroutine that feeds process output into emu.
	// vt.SafeEmulator's own mutex only protects the call that hands out the
	// pointer, not the lifetime of the cell it points to, so a reader that
	// keeps looking at the cell after the call returns can still race with
	// the next Write. Emulator.Write mutates cells in place (see
	// go test -race in the report), so every reader here copies out the
	// fields it needs while still holding emuMu.
	emuMu sync.Mutex

	timeout time.Duration

	recordFile *os.File
	recordMu   sync.Mutex

	waitErr   error
	waitExit  int
	waitDone  chan struct{}
	copyDone  chan struct{}
	closeOnce sync.Once
}

// Start starts binary under a PTY of the given size for use within a Go
// test. It registers a cleanup that closes the screen (killing the process
// if it is still running) when the test finishes.
func Start(tb testing.TB, binary string, args []string, cols, rows int, opts ...Option) *Screen {
	tb.Helper()
	s, err := start(tb, binary, args, cols, rows, opts...)
	if err != nil {
		tb.Fatalf("screen.Start: %v", err)
		return nil
	}
	tb.Cleanup(s.Close)
	return s
}

// StartDetached starts binary under a PTY of the given size without a
// testing.TB, for long-running use such as cmd/harness-drive. The caller is
// responsible for calling Close.
func StartDetached(binary string, args []string, cols, rows int, opts ...Option) (*Screen, error) {
	return start(nil, binary, args, cols, rows, opts...)
}

func start(tb testing.TB, binary string, args []string, cols, rows int, opts ...Option) (*Screen, error) {
	cfg := newConfig(opts)

	home := ""
	if tb != nil {
		home = tb.TempDir()
	} else {
		dir, err := os.MkdirTemp("", "harness-screen-home-*")
		if err != nil {
			return nil, fmt.Errorf("create home dir: %w", err)
		}
		home = dir
	}

	env := buildEnv(cfg, home)

	cmd := exec.Command(binary, args...)
	cmd.Env = env

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)}) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("start pty: %w", err)
	}

	s := &Screen{
		tb:       tb,
		cols:     cols,
		rows:     rows,
		cmd:      cmd,
		ptmx:     ptmx,
		emu:      vt.NewSafeEmulator(cols, rows),
		timeout:  cfg.timeout,
		waitDone: make(chan struct{}),
		copyDone: make(chan struct{}),
	}
	s.emu.SetScrollbackSize(vt.DefaultScrollbackSize)

	if cfg.recordPath != "" {
		f, err := os.Create(cfg.recordPath) //nolint:gosec
		if err != nil {
			_ = ptmx.Close()
			_ = cmd.Process.Kill()
			return nil, fmt.Errorf("create record file: %w", err)
		}
		s.recordFile = f
	}

	// PTY output -> emulator, tee'd to the record file if any.
	var ptyReader io.Reader = ptmx
	if s.recordFile != nil {
		ptyReader = io.TeeReader(ptmx, s.recordFile)
	}
	go func() {
		defer close(s.copyDone)
		buf := make([]byte, 4096)
		for {
			n, rerr := ptyReader.Read(buf)
			if n > 0 {
				s.emuMu.Lock()
				_, _ = s.emu.Write(buf[:n]) //nolint:errcheck
				s.emuMu.Unlock()
			}
			if rerr != nil {
				return
			}
		}
	}()

	// Encoded key/text bytes from the emulator (from SendKey/SendText) -> PTY.
	go func() {
		_, _ = io.Copy(ptmx, s.emu) //nolint:errcheck
	}()

	go func() {
		err := cmd.Wait()
		s.waitErr = err
		if cmd.ProcessState != nil {
			s.waitExit = cmd.ProcessState.ExitCode()
		}
		close(s.waitDone)
	}()

	return s, nil
}

func buildEnv(cfg *config, home string) []string {
	m := map[string]string{}
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	// Forced defaults, applied before user overrides.
	m["TERM"] = "xterm-256color"
	m["COLORTERM"] = "truecolor"
	m["HOME"] = home
	delete(m, "NO_COLOR")

	for k, v := range cfg.env {
		m[k] = v
	}
	for k := range cfg.unsetEnv {
		delete(m, k)
	}

	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

// Send sends raw text to the process as if typed by a user. It supports the
// escapes \r, \n, \t, and \e (ESC) as two-character sequences in data;
// everything else is sent literally.
func (s *Screen) Send(data string) {
	text := unescape(data)
	s.recordIn(text)
	s.emu.SendText(text)
}

func unescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'r':
				b.WriteByte('\r')
				i++
				continue
			case 'n':
				b.WriteByte('\n')
				i++
				continue
			case 't':
				b.WriteByte('\t')
				i++
				continue
			case 'e':
				b.WriteByte('\x1b')
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// namedKeys maps SendKey's friendly names to key events, encoded exactly as
// a real terminal would encode them (legacy CSI/control bytes).
var namedKeys = map[string]vt.KeyPressEvent{
	"enter":     {Code: vt.KeyEnter},
	"escape":    {Code: vt.KeyEscape},
	"esc":       {Code: vt.KeyEscape},
	"tab":       {Code: vt.KeyTab},
	"shift+tab": {Code: vt.KeyTab, Mod: vt.ModShift},
	"backspace": {Code: vt.KeyBackspace},
	"up":        {Code: vt.KeyUp},
	"down":      {Code: vt.KeyDown},
	"left":      {Code: vt.KeyLeft},
	"right":     {Code: vt.KeyRight},
	"home":      {Code: vt.KeyHome},
	"end":       {Code: vt.KeyEnd},
	"pgup":      {Code: vt.KeyPgUp},
	"pgdown":    {Code: vt.KeyPgDown},
	"delete":    {Code: vt.KeyDelete},
	"insert":    {Code: vt.KeyInsert},
	"space":     {Code: vt.KeySpace},
}

// SendKey sends one or more key presses to the process, encoded exactly as a
// real terminal would send them. Names are friendly identifiers such as
// "enter", "escape", "tab", "shift+tab", "ctrl+c", "ctrl+r", "up", "down",
// "left", "right", "backspace", or a single printable rune (e.g. "a", "p").
func (s *Screen) SendKey(names ...string) {
	for _, name := range names {
		ev, err := parseKeyName(name)
		if err != nil {
			if s.tb != nil {
				s.tb.Fatalf("screen.SendKey: %v", err)
			}
			continue
		}
		s.recordIn(fmt.Sprintf("<%s>", name))
		s.emu.SendKey(ev)
		if ev.Code == vt.KeyEscape && ev.Mod == 0 {
			// A bare ESC immediately followed by another byte in the
			// same read is, in legacy key mode, an Alt-modified key
			// ("esc" then "ctrl+c" arrives as alt+ctrl+c). A person
			// cannot type two keys inside the parser's escape timeout;
			// pausing here keeps scripts describing human keypresses.
			time.Sleep(escGap)
		}
	}
}

// escGap is how long SendKey waits after a bare Escape before the next
// key. Bubbletea's input parser treats ESC+byte in one read as Alt+key.
const escGap = 100 * time.Millisecond

func parseKeyName(name string) (vt.KeyPressEvent, error) {
	if ev, ok := namedKeys[name]; ok {
		return ev, nil
	}
	if strings.HasPrefix(name, "ctrl+") {
		rest := strings.TrimPrefix(name, "ctrl+")
		r := []rune(rest)
		if len(r) == 1 {
			return vt.KeyPressEvent{Code: r[0], Mod: vt.ModCtrl}, nil
		}
	}
	if strings.HasPrefix(name, "alt+") {
		rest := strings.TrimPrefix(name, "alt+")
		r := []rune(rest)
		if len(r) == 1 {
			return vt.KeyPressEvent{Code: r[0], Mod: vt.ModAlt}, nil
		}
	}
	r := []rune(name)
	if len(r) == 1 {
		return vt.KeyPressEvent{Code: r[0]}, nil
	}
	return vt.KeyPressEvent{}, fmt.Errorf("unknown key name %q", name)
}

func (s *Screen) recordIn(data string) {
	if s.recordFile == nil {
		return
	}
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	_, _ = io.WriteString(s.recordFile, "\x00IN:"+data)
}

// WaitFor blocks until the current viewport matches pattern (a string,
// matched as a substring across the joined rows, or a *regexp.Regexp), or
// timeout elapses. On timeout the returned error's message includes a ruler
// line and every row, to make the failure diagnosable without a debugger.
func (s *Screen) WaitFor(pattern any, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = s.timeout
	}
	deadline := time.Now().Add(timeout)
	var matches func(string) bool
	switch p := pattern.(type) {
	case string:
		matches = func(text string) bool { return strings.Contains(text, p) }
	case *regexp.Regexp:
		matches = p.MatchString
	default:
		return fmt.Errorf("screen.WaitFor: unsupported pattern type %T", pattern)
	}

	for {
		rows := s.Viewport()
		if matches(strings.Join(rows, "\n")) {
			s.settle(deadline)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("screen.WaitFor: timed out after %s waiting for %v\n%s", timeout, pattern, dumpRows(rows, s.cols))
		}
		time.Sleep(15 * time.Millisecond)
	}
}

// settle waits until the screen has stopped changing for settleQuiet, or
// the deadline passes. Bubbletea inserts committed lines immediately but
// repaints the live region on its own frame tick, so the instant a WAIT
// pattern appears the frame under it can still be the previous one (a
// spinner row under a finished turn's summary). A person never sees that
// frame; a dump taken inside it does.
func (s *Screen) settle(deadline time.Time) {
	last := strings.Join(s.Viewport(), "\n")
	quietSince := time.Now()
	for time.Now().Before(deadline) {
		time.Sleep(15 * time.Millisecond)
		now := strings.Join(s.Viewport(), "\n")
		if now != last {
			last = now
			quietSince = time.Now()
			continue
		}
		if time.Since(quietSince) >= settleQuiet {
			return
		}
	}
}

// settleQuiet is how long the screen must stay unchanged after a WAIT
// match before WaitFor returns. Longer than one Bubbletea frame (16ms),
// shorter than the 80ms spinner tick so a busy spinner does not hold WAIT
// hostage: while the spinner animates the frame changes every 80ms, so the
// 60ms window still closes between ticks.
const settleQuiet = 60 * time.Millisecond

func dumpRows(rows []string, cols int) string {
	var b strings.Builder
	b.WriteString(ruler(cols))
	b.WriteByte('\n')
	for i, r := range rows {
		fmt.Fprintf(&b, "%2d: %s\n", i, r)
	}
	return b.String()
}

func ruler(cols int) string {
	var b strings.Builder
	for i := 0; i < cols; i++ {
		if i%10 == 0 {
			b.WriteString(fmt.Sprintf("%d", i/10%10))
		} else if i%5 == 0 {
			b.WriteByte('-')
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// checkWidth enforces the width invariant: every row must render at no more
// than s.cols columns wide. When a testing.TB is attached the test fails
// immediately; otherwise the violation is reported by returning it via err.
func (s *Screen) checkWidth(rows []string) {
	for i, row := range rows {
		if w := ansi.StringWidth(row); w > s.cols {
			msg := fmt.Sprintf("screen: row %d exceeds terminal width (%d > %d): %q", i, w, s.cols, row)
			if s.tb != nil {
				s.tb.Helper()
				s.tb.Errorf("%s", msg)
			}
		}
	}
}

// Viewport returns every visible row, untrimmed: exactly s.rows rows, each
// padded with trailing spaces removed only where the emulator itself leaves
// cells empty.
func (s *Screen) Viewport() []string {
	s.emuMu.Lock()
	rows := make([]string, s.rows)
	for y := 0; y < s.rows; y++ {
		rows[y] = s.cellRowString(y)
	}
	s.emuMu.Unlock()
	s.checkWidth(rows)
	return rows
}

// Rows returns the visible rows with trailing blank rows and trailing
// spaces on each row trimmed.
func (s *Screen) Rows() []string {
	rows := s.Viewport()
	trimmed := make([]string, len(rows))
	for i, r := range rows {
		trimmed[i] = strings.TrimRight(r, " ")
	}
	for len(trimmed) > 0 && trimmed[len(trimmed)-1] == "" {
		trimmed = trimmed[:len(trimmed)-1]
	}
	return trimmed
}

// CursorRow returns the 0-based row the cursor is parked on.
func (s *Screen) CursorRow() int {
	s.emuMu.Lock()
	defer s.emuMu.Unlock()
	return s.emu.CursorPosition().Y
}

// OccupiedHeight returns the index of the last non-blank row plus 1. It is
// never trimmed by width or by Rows()'s blank-row trimming; it always scans
// the full s.rows x s.cols grid. It returns 0 if every row is blank.
func (s *Screen) OccupiedHeight() int {
	s.emuMu.Lock()
	defer s.emuMu.Unlock()
	last := -1
	for y := 0; y < s.rows; y++ {
		if strings.TrimRight(s.cellRowString(y), " ") != "" {
			last = y
		}
	}
	return last + 1
}

// Scrollback returns the lines that have scrolled above the viewport, if
// the emulator has any (the alternate screen has none).
func (s *Screen) Scrollback() []string {
	s.emuMu.Lock()
	defer s.emuMu.Unlock()
	n := s.emu.ScrollbackLen()
	out := make([]string, n)
	for y := 0; y < n; y++ {
		var b strings.Builder
		for x := 0; x < s.cols; x++ {
			c := s.emu.ScrollbackCellAt(x, y)
			if c == nil {
				b.WriteByte(' ')
				continue
			}
			if c.Content == "" {
				if c.Width == 0 {
					continue
				}
				b.WriteByte(' ')
				continue
			}
			b.WriteString(c.Content)
		}
		out[y] = strings.TrimRight(b.String(), " ")
	}
	return out
}

func (s *Screen) cellRowString(y int) string {
	var b strings.Builder
	for x := 0; x < s.cols; x++ {
		c := s.emu.CellAt(x, y)
		if c == nil {
			b.WriteByte(' ')
			continue
		}
		if c.Content == "" {
			if c.Width == 0 {
				// Continuation cell of a wide rune; already accounted for.
				continue
			}
			b.WriteByte(' ')
			continue
		}
		b.WriteString(c.Content)
	}
	return b.String()
}

// Resize resizes the PTY and the emulator. The kernel delivers SIGWINCH to
// the process automatically.
func (s *Screen) Resize(cols, rows int) {
	s.emuMu.Lock()
	s.cols = cols
	s.rows = rows
	s.emu.Resize(cols, rows)
	s.emuMu.Unlock()
	_ = pty.Setsize(s.ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)}) //nolint:errcheck,gosec
}

// Exit sends nothing and waits for the process to exit on its own, up to the
// screen's timeout. It returns the process's exit code.
func (s *Screen) Exit() (int, error) {
	select {
	case <-s.waitDone:
		return s.waitExit, s.waitErr
	case <-time.After(s.timeout):
		return -1, fmt.Errorf("screen.Exit: process did not exit within %s", s.timeout)
	}
}

// Close kills the process if it is still running and releases the PTY and
// any record file. It is safe to call more than once.
func (s *Screen) Close() {
	s.closeOnce.Do(func() {
		if s.cmd.Process != nil {
			select {
			case <-s.waitDone:
			default:
				_ = s.cmd.Process.Kill()
			}
		}
		select {
		case <-s.waitDone:
		case <-time.After(2 * time.Second):
		}
		_ = s.ptmx.Close()
		select {
		case <-s.copyDone:
		case <-time.After(2 * time.Second):
		}
		if s.recordFile != nil {
			_ = s.recordFile.Close()
		}
	})
}

// repoTestdataGoldenDir returns the repository's top-level testdata/golden
// directory (github.com/andrepato/harness/testdata/golden), resolved from
// this source file's own location rather than the test binary's working
// directory, so Golden lands goldens in one place regardless of which
// package's test invokes it.
func repoTestdataGoldenDir() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return filepath.Join("testdata", "golden")
	}
	// this file is internal/testkit/screen/screen.go; the repo root is three
	// directories up.
	root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(thisFile))))
	return filepath.Join(root, "testdata", "golden")
}

// Golden compares Rows(), joined with "\n", against
// testdata/golden/<name>.txt. Set UPDATE=1 in the environment to rewrite the
// golden file instead of comparing.
func (s *Screen) Golden(tb testing.TB, name string) {
	tb.Helper()
	got := strings.Join(s.Rows(), "\n")
	path := filepath.Join(repoTestdataGoldenDir(), name+".txt")

	if os.Getenv("UPDATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			tb.Fatalf("screen.Golden: mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(got+"\n"), 0o644); err != nil { //nolint:gosec
			tb.Fatalf("screen.Golden: write: %v", err)
		}
		return
	}

	want, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		tb.Fatalf("screen.Golden: read %s: %v (run with UPDATE=1 to create it)", path, err)
	}
	if got+"\n" != string(want) && got != string(want) {
		tb.Errorf("screen.Golden: %s mismatch\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}
