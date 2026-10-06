// Background shells: the registry behind internal/tools/backgroundshell.go
// (bash_background, bash_output, kill_shell) and the /bashes summary,
// porting src/agent/background-shell.ts's BackgroundShells class,
// renderShellList, and the onUpdate/absorb protocol.
//
// # Why the output is a ring buffer
//
// A watch build emits output forever. Keeping all of it is a memory leak
// with extra steps, and the last few hundred lines are the only ones
// anyone reads. The buffer is bounded and drops from the front, which is
// also what `tail -f` does - the behavior people already expect from a
// log.
//
// # Why reads are incremental
//
// bash_output returns only what is new since the last read. Returning the
// whole buffer each time would put the same lines into context on every
// poll, which on a small window is a way to end the conversation by
// checking on a build. The cursor is per-shell and advances on read.
//
// # Deviation from the brief
//
// The brief's Shell/ShellStatus types and the ShellManager interface
// bash_background/bash_output/kill_shell need live in internal/tools
// (backgroundshell.go), not here. internal/agent already imports
// internal/tools elsewhere (session.go, todo.go, for TodoItem/TodoStatus);
// internal/tools importing internal/agent back - which the brief's literal
// constructor signature BackgroundShellTools(*agent.BackgroundShells, ...)
// would require - is an import cycle. BackgroundShells here is exactly the
// class background-shell.ts describes; it just returns and accepts
// tools.Shell/tools.ShellStatus, and satisfies tools.ShellManager
// structurally.
package agent

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andrepato/harness/internal/crash"
	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/plural"
	"github.com/andrepato/harness/internal/tools"
)

// Shell and ShellStatus are aliases of the tools package's types, matching
// the pattern todo.go already uses for TodoItem/TodoStatus: the type that
// crosses into a model-facing tool's schema is owned by internal/tools,
// and the store here just holds what that layer produces.
type (
	Shell       = tools.Shell
	ShellStatus = tools.ShellStatus
)

const (
	ShellRunning = tools.ShellRunning
	ShellExited  = tools.ShellExited
	ShellKilled  = tools.ShellKilled
	ShellFailed  = tools.ShellFailed
)

// shellEntry is one shell's live state: the public Shell snapshot fields
// plus the ring buffer and cursor bookkeeping, mirroring background-shell.ts's
// Entry. Guarded by mu since, unlike the single-threaded TS original, the
// absorb goroutine (fed by env.Exec's stdout/stderr pumps) and Read/Get/Kill
// calls from tool executions run concurrently.
type shellEntry struct {
	id        string
	command   string
	startedAt time.Time

	mu       sync.Mutex
	status   ShellStatus
	exitCode *int
	endedAt  *time.Time

	// text is the current output window, maintained per the update
	// protocol. lines is text split on "\n". cursor is an index into the
	// total line stream, not into lines. droppedLines is lines that fell
	// off the front, so a reader can be told what it missed.
	text         string
	lines        []string
	cursor       int
	droppedLines int

	cancel context.CancelFunc
}

// BackgroundShells is the registry of background shells for one session,
// mirroring background-shell.ts's BackgroundShells class.
type BackgroundShells struct {
	mu     sync.Mutex
	shells map[string]*shellEntry
	order  []string
	nextID int
}

// NewBackgroundShells returns an empty registry.
func NewBackgroundShells() *BackgroundShells {
	return &BackgroundShells{shells: map[string]*shellEntry{}, nextID: 1}
}

// splitLines splits without the empty trailing element a final newline
// produces, mirroring background-shell.ts's splitLines.
func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	out := strings.Split(text, "\n")
	if out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

func countLines(text string) int { return len(splitLines(text)) }

// Start begins running command in the background and returns immediately.
//
// ctx is checked only for an early-cancellation refusal; it is deliberately
// NOT the context env.Exec runs under. Each shell gets its own
// context.CancelFunc derived from context.Background(), so the tool call
// returning (which cancels ctx, the tool's own call-scoped context) does
// not kill the shell - the whole point of running it in the background.
//
// The exec.Env.Exec call is not awaited before returning - that is the
// whole point - but its result IS attached to via a goroutine, so an exit
// updates the record rather than leaving a dead shell reported as running
// forever.
func (b *BackgroundShells) Start(ctx context.Context, command string, env *execenv.Env) (*Shell, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	b.mu.Lock()
	id := "bash_" + strconv.Itoa(b.nextID)
	b.nextID++
	entry := &shellEntry{
		id:        id,
		command:   command,
		startedAt: time.Now(),
		status:    ShellRunning,
	}
	runCtx, cancel := context.WithCancel(context.Background())
	entry.cancel = cancel
	b.shells[id] = entry
	b.order = append(b.order, id)
	b.mu.Unlock()

	// A background command runs in the OS sandbox like a foreground one
	// (bash_background has no dangerouslyDisableSandbox: excludedCommands
	// is how one leaves it).
	var sandbox execenv.CommandSandbox
	if env.Sandbox != nil {
		sandbox = env.Sandbox.ForCommand(command, false)
	}
	crash.Go(func() {
		result, err := env.Exec(runCtx, command, execenv.ExecOptions{
			InheritEnv: true,
			Sandbox:    sandbox,
			OnUpdate: func(update execenv.ShellOutputUpdate) {
				entry.absorb(update)
			},
		})

		entry.mu.Lock()
		defer entry.mu.Unlock()
		if entry.status == ShellKilled {
			// Kill already set status/endedAt; do not overwrite them with
			// whatever env.Exec happened to return once cancellation
			// propagated.
			return
		}
		now := time.Now()
		entry.endedAt = &now
		if err == nil {
			entry.status = ShellExited
			code := result.ExitCode
			entry.exitCode = &code
		} else {
			entry.status = ShellFailed
		}
	})

	snap := entry.snapshot()
	return &snap, nil
}

// absorb folds one output update into the entry's buffer.
//
// Four kinds, and they are NOT all appends - this was ported from a class
// whose own comment says it was verified against a live exec, because
// getting it wrong duplicates or silently drops output:
//
//   - replace hands over the whole current window, under Output.Text (not
//     Text, which is the append field).
//   - append adds Text to the end.
//   - slide means the window scrolled: Drop bytes fell off the front and
//     Text was added to the end. Appending without dropping would
//     duplicate the retained tail.
//   - metadata carries no output at all.
//
// Lines lost off the front are counted rather than forgotten, so an
// incremental reader can be told what it missed instead of quietly
// skipping it.
//
// Drop is clamped to len(text): once this entry's own MAX_LINES cap (below)
// has trimmed text shorter than the source's own tracked view, a slide
// computed against that longer view can name a Drop past the end of our
// shorter copy. JS's String.slice clamps an out-of-range index silently;
// Go's slice expression panics on one, so the clamp is required to
// reproduce - not change - that behavior.
func (e *shellEntry) absorb(update execenv.ShellOutputUpdate) {
	e.mu.Lock()
	defer e.mu.Unlock()

	switch update.Kind {
	case execenv.UpdateReplace:
		e.text = update.Output.Text
	case execenv.UpdateAppend:
		e.text += update.Text
	case execenv.UpdateSlide:
		drop := update.Drop
		if drop > len(e.text) {
			drop = len(e.text)
		}
		shed := e.text[:drop]
		e.droppedLines += countLines(shed)
		e.text = e.text[drop:] + update.Text
	case execenv.UpdateMetadata:
		return
	default:
		return
	}

	// Cap here as well as at the source: slide only fires once the source
	// hits its own limit, and a command that never reaches it would
	// otherwise grow this buffer without bound.
	//
	// background-shell.ts rebuilds entry.text here with lines.join("\n"),
	// which drops the buffer's trailing newline. The next "append" chunk
	// always starts a fresh line on the assumption that the text it is
	// being concatenated onto already ends in "\n" (that separator is what
	// the source's own diffing relies on); losing it glues that append's
	// first line onto the last retained line, merging two lines into one -
	// verified directly against a live command producing 1000 sequential
	// lines, which silently lost lines at every trim boundary this way.
	// dropFrontLines takes the same byte range splitLines(text)[excess:]
	// denotes, but by slicing the original string rather than rejoining a
	// tokenized copy, so whatever trailing newline (or partial last line)
	// was actually there survives untouched. This is a deliberate
	// deviation from the TS source to avoid that corruption; the resulting
	// line contents and droppedLines count are otherwise identical.
	lines := splitLines(e.text)
	if len(lines) > tools.MaxBackgroundShellLines {
		excess := len(lines) - tools.MaxBackgroundShellLines
		e.text = dropFrontLines(e.text, excess)
		e.droppedLines += excess
		lines = splitLines(e.text)
	}
	e.lines = lines
}

// dropFrontLines removes the first n complete (newline-terminated) lines
// from the front of text, returning everything after the nth newline
// untouched - including a trailing newline or a trailing partial line, if
// either is present. Equivalent to splitLines(text)[n:] rejoined, but
// without discarding that trailing structure.
func dropFrontLines(text string, n int) string {
	idx := 0
	for i := 0; i < n; i++ {
		pos := strings.IndexByte(text[idx:], '\n')
		if pos == -1 {
			return ""
		}
		idx += pos + 1
	}
	return text[idx:]
}

// Read returns everything new since id's last read, advancing the cursor.
func (b *BackgroundShells) Read(id string) (lines []string, missed int, shell Shell, ok bool) {
	entry := b.get(id)
	if entry == nil {
		return nil, 0, Shell{}, false
	}

	entry.mu.Lock()
	defer entry.mu.Unlock()

	// droppedLines is the index of the first line still held, so anything
	// the cursor points at below that is gone. Say how much rather than
	// silently skipping it.
	produced := entry.droppedLines + len(entry.lines)
	missed = max(0, entry.droppedLines-entry.cursor)
	from := max(0, entry.cursor-entry.droppedLines)
	var out []string
	if from < len(entry.lines) {
		out = append([]string{}, entry.lines[from:]...)
	}
	entry.cursor = produced

	return out, missed, entry.snapshotLocked(), true
}

// Kill stops a running shell. A shell that already finished is left alone
// and returned as-is - "Killed" for one already exited would be a small
// lie the caller (kill_shell) is careful not to tell.
func (b *BackgroundShells) Kill(id string) (Shell, bool) {
	entry := b.get(id)
	if entry == nil {
		return Shell{}, false
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.status == ShellRunning {
		entry.status = ShellKilled
		now := time.Now()
		entry.endedAt = &now
		entry.cancel()
	}
	return entry.snapshotLocked(), true
}

// Get returns the current snapshot without reading output.
func (b *BackgroundShells) Get(id string) (Shell, bool) {
	entry := b.get(id)
	if entry == nil {
		return Shell{}, false
	}
	return entry.snapshot(), true
}

// List returns every shell, running or finished, in start order.
func (b *BackgroundShells) List() []Shell {
	b.mu.Lock()
	order := append([]string{}, b.order...)
	shells := make([]*shellEntry, 0, len(order))
	for _, id := range order {
		shells = append(shells, b.shells[id])
	}
	b.mu.Unlock()

	out := make([]Shell, 0, len(shells))
	for _, e := range shells {
		out = append(out, e.snapshot())
	}
	return out
}

// KillAll stops everything still running. Called on exit so a dev server
// does not outlive the session as a port nobody has a handle on anymore.
func (b *BackgroundShells) KillAll() {
	b.mu.Lock()
	shells := make([]*shellEntry, 0, len(b.shells))
	for _, e := range b.shells {
		shells = append(shells, e)
	}
	b.mu.Unlock()

	for _, e := range shells {
		e.mu.Lock()
		if e.status == ShellRunning {
			e.status = ShellKilled
			now := time.Now()
			e.endedAt = &now
			e.cancel()
		}
		e.mu.Unlock()
	}
}

func (b *BackgroundShells) get(id string) *shellEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.shells[id]
}

// snapshot copies the entry's public fields into a Shell value, acquiring
// the lock itself.
func (e *shellEntry) snapshot() Shell {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.snapshotLocked()
}

// snapshotLocked is snapshot without acquiring the lock, for callers that
// already hold it.
func (e *shellEntry) snapshotLocked() Shell {
	return Shell{
		ID:        e.id,
		Command:   e.command,
		Status:    e.status,
		ExitCode:  e.exitCode,
		StartedAt: e.startedAt,
		EndedAt:   e.endedAt,
		Dropped:   e.droppedLines,
	}
}

// RenderShellList renders the /bashes summary, mirroring
// background-shell.ts's renderShellList.
func RenderShellList(shells []Shell) string {
	if len(shells) == 0 {
		return "No background shells."
	}
	running := 0
	for _, s := range shells {
		if s.Status == ShellRunning {
			running++
		}
	}
	lines := make([]string, 0, len(shells)+2)
	lines = append(lines, plural.Count(len(shells), "shell")+", "+strconv.Itoa(running)+" running", "")
	for _, s := range shells {
		lines = append(lines, "  "+describeShell(s))
	}
	return strings.Join(lines, "\n")
}

// describeShell renders one shell exactly as background-shell.ts's
// describe() does: "bash_1  running (exit 0)  3s  sleep 5".
func describeShell(s Shell) string {
	end := time.Now()
	if s.EndedAt != nil {
		end = *s.EndedAt
	}
	seconds := int(end.Sub(s.StartedAt).Round(time.Second).Seconds())
	code := ""
	if s.ExitCode != nil {
		code = " (exit " + strconv.Itoa(*s.ExitCode) + ")"
	}
	return s.ID + "  " + string(s.Status) + code + "  " + strconv.Itoa(seconds) + "s  " + s.Command
}
