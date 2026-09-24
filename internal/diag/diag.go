// Package diag is the harness's diagnostics log: one file per run under
// ~/.harness/logs, written at Info level always and at Debug level with
// --debug or HARNESS_DEBUG=1. It exists so "it's stuck" can be answered
// from a file rather than reproduced: startup phases with elapsed time,
// every MCP server's connect outcome and duration, every harness event
// (turns, tool calls, retries, compaction), and the exit code.
//
// The logger is process-global so packages log without plumbing; before
// Start it discards everything, so tests and subcommands that never call
// Start pay nothing.
package diag

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// keepFiles is how many run logs survive pruning.
const keepFiles = 30

var (
	current atomic.Pointer[slog.Logger]
	startMu sync.Mutex
	started = time.Now()
)

func init() {
	current.Store(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// L returns the process logger.
func L() *slog.Logger { return current.Load() }

// Since is the elapsed time since the process started, for phase timing.
func Since() time.Duration { return time.Since(started).Round(time.Millisecond) }

// Dir is where run logs live: $HARNESS_LOG_DIR, else ~/.harness/logs.
func Dir() string {
	if d := os.Getenv("HARNESS_LOG_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "harness-logs")
	}
	return filepath.Join(home, ".harness", "logs")
}

// Start opens this run's log file and installs it as the process logger.
// label is folded into the file name (a session id prefix, or the
// subcommand). debug lowers the level to Debug; HARNESS_DEBUG=1 does the
// same. It returns the file's path and a close func. On any failure it
// keeps the discarding logger and returns the error: diagnostics must
// never stop the harness from starting.
func Start(label string, debug bool) (path string, closeFn func(), err error) {
	startMu.Lock()
	defer startMu.Unlock()

	dir := Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", func() {}, fmt.Errorf("diag: %w", err)
	}
	label = sanitize(label)
	name := fmt.Sprintf("harness-%s-%d", time.Now().Format("20060102-150405"), os.Getpid())
	if label != "" {
		name += "-" + label
	}
	path = filepath.Join(dir, name+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", func() {}, fmt.Errorf("diag: %w", err)
	}

	level := slog.LevelInfo
	if debug || os.Getenv("HARNESS_DEBUG") == "1" {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: level}))
	current.Store(logger)
	logger.Info("start", "pid", os.Getpid(), "args", strings.Join(os.Args[1:], " "), "level", level.String())

	prune(dir, path)

	return path, func() {
		logger.Info("exit", "elapsed", Since())
		current.Store(slog.New(slog.NewTextHandler(io.Discard, nil)))
		_ = f.Close()
	}, nil
}

// Latest returns the newest run log in Dir, or "" when there is none.
func Latest() string {
	entries, err := os.ReadDir(Dir())
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "harness-") && strings.HasSuffix(e.Name(), ".log") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names) // the timestamp in the name sorts chronologically
	return filepath.Join(Dir(), names[len(names)-1])
}

// prune deletes the oldest run logs beyond keepFiles, never the current.
func prune(dir, keep string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "harness-") && strings.HasSuffix(e.Name(), ".log") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > keepFiles {
		victim := filepath.Join(dir, names[0])
		names = names[1:]
		if victim == keep {
			continue
		}
		_ = os.Remove(victim)
	}
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
		if b.Len() >= 16 {
			break
		}
	}
	return b.String()
}
