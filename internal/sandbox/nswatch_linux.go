//go:build linux

package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/andrepato/harness/internal/crash"
	"github.com/andrepato/harness/internal/execenv"
	"golang.org/x/sys/unix"
)

// defaultNSWait bounds how long teardown waits for a PID namespace to empty.
const defaultNSWait = 5 * time.Second

// nsWatch follows the PID namespace one worker command runs in, so teardown
// can wait until it is empty rather than hope the kernel got there.
//
// bwrap --info-fd writes the host PID of the namespace's init ("child-pid")
// before the command starts. nsWatch opens a pidfd for it at once (a pidfd
// names that process, never a recycled PID) and later waits for the pidfd to
// become readable. A pidfd is readable once the process has exited, and the
// kernel does that only after zap_pid_ns_processes has killed and reaped
// every other task in the namespace (docs/efficiency/isolated-shell-activation.md).
// Readable therefore means empty.
type nsWatch struct {
	m       *Manager
	r, w    *os.File // pipe: bwrap writes its info to w, kiln reads r
	started atomic.Bool
	ok      atomic.Bool   // drain confirmed the namespace empty
	noInfo  bool          // the pipe closed before bwrap reported anything
	done    chan struct{} // closed when the reader goroutine finished
	// mu guards pidfd: Exec's cleanup and Manager.Shutdown can both reach
	// release while the other is still waiting on the namespace.
	mu    sync.Mutex
	pidfd int   // -1 when none was opened, or once released
	gone  bool  // init already gone when the pidfd was opened
	err   error // reading or opening failed
}

func (m *Manager) pidfdOpen(pid int) (int, error) {
	if m.opts.PidfdOpen != nil {
		return m.opts.PidfdOpen(pid)
	}
	return unix.PidfdOpen(pid, 0)
}

func (m *Manager) pidfdSignal(fd int, sig syscall.Signal) error {
	if m.opts.PidfdSignal != nil {
		return m.opts.PidfdSignal(fd, sig)
	}
	return unix.PidfdSendSignal(fd, sig, nil, 0)
}

func (m *Manager) nsWaitTimeout() time.Duration {
	if m.opts.NSWaitTimeout > 0 {
		return m.opts.NSWaitTimeout
	}
	return defaultNSWait
}

// newNSWatch makes the pipe bwrap's --info-fd will use (fd 3 in the command).
func (m *Manager) newNSWatch() (*nsWatch, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("sandbox: namespace pipe: %w", err)
	}
	n := &nsWatch{m: m, r: r, w: w, done: make(chan struct{}), pidfd: -1}
	m.mu.Lock()
	if m.nsWatches == nil {
		m.nsWatches = map[*nsWatch]struct{}{}
	}
	m.nsWatches[n] = struct{}{}
	m.mu.Unlock()
	return n, nil
}

// start reads bwrap's info and opens the pidfd, in the background: bwrap
// writes it as soon as the namespace exists, and kiln must not block the
// command's own start on it.
func (n *nsWatch) start() {
	n.started.Store(true)
	crash.Go(func() {
		defer close(n.done)
		var info struct {
			ChildPID int `json:"child-pid"`
		}
		if err := json.NewDecoder(n.r).Decode(&info); err != nil {
			// EOF: bwrap ended before it made the namespace (or was killed
			// first, which stopped() tells apart by how bwrap ended).
			n.noInfo = errors.Is(err, io.EOF)
			n.err = err
			return
		}
		if info.ChildPID <= 0 {
			n.err = errors.New("bwrap reported no child-pid")
			return
		}
		fd, err := n.m.pidfdOpen(info.ChildPID)
		if err != nil {
			if errors.Is(err, syscall.ESRCH) {
				n.gone = true // already exited and reaped: the namespace is empty
				return
			}
			n.err = fmt.Errorf("pidfd_open(%d): %w", info.ChildPID, err)
			return
		}
		n.mu.Lock()
		n.pidfd = fd
		n.mu.Unlock()
		// The PID must still be the namespace's init, not a recycled PID.
		if st, err := os.ReadFile("/proc/" + strconv.Itoa(info.ChildPID) + "/status"); err == nil {
			if !isNSInit(string(st)) {
				n.err = fmt.Errorf("pid %d is not a namespace init", info.ChildPID)
			}
		}
	})
}

// isNSInit reports whether a /proc status file belongs to a PID namespace's
// init: the innermost PID on its "NSpid:" line is exactly 1.
func isNSInit(status string) bool {
	for _, l := range strings.Split(status, "\n") {
		if strings.HasPrefix(l, "NSpid:") {
			f := strings.Fields(l)
			return len(f) > 1 && f[len(f)-1] == "1"
		}
	}
	return true // kernel without NSpid: cannot check
}

// stopped waits up to timeout for the namespace to be empty. cmdState is
// the bwrap process's state when it has exited, nil if unknown.
func (n *nsWatch) stopped(cmdState *os.ProcessState, timeout time.Duration) error {
	if !n.started.Load() {
		return nil // never ran: no namespace
	}
	deadline := time.Now().Add(timeout)
	select {
	case <-n.done:
	case <-time.After(timeout):
		return fmt.Errorf("sandbox: namespace of a worker command not reported within %s", timeout)
	}
	if n.err != nil {
		// No info and bwrap ended normally (it failed before the
		// namespace existed) leaves nothing to wait for. If it was killed,
		// the namespace may exist unseen.
		if n.noInfo && cmdState != nil && !signaled(cmdState) {
			return nil
		}
		return fmt.Errorf("sandbox: cannot confirm a worker command's processes stopped: %w", n.err)
	}
	if n.gone {
		return nil
	}
	// Work on a duplicate: release may close n.pidfd meanwhile, and a closed
	// number can be reused by another file before the poll below.
	n.mu.Lock()
	if n.pidfd < 0 {
		n.mu.Unlock()
		return nil // released, which happens only once the namespace was confirmed empty
	}
	pidfd, err := unix.Dup(n.pidfd)
	n.mu.Unlock()
	if err != nil {
		return fmt.Errorf("sandbox: waiting for a worker command's namespace: %w", err)
	}
	defer unix.Close(pidfd)
	// bwrap has exited (or teardown has begun), so nothing in the namespace
	// may outlive this. --die-with-parent alone does not cover a bwrap
	// killed before its init armed PR_SET_PDEATHSIG. Killing init makes the
	// kernel kill the rest. An error here (ESRCH: already exited) is left to
	// the poll to settle.
	_ = n.m.pidfdSignal(pidfd, syscall.SIGKILL)
	for {
		left := time.Until(deadline)
		if left < 0 {
			left = 0
		}
		fds := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}
		k, err := unix.Poll(fds, int(left.Milliseconds()))
		switch {
		case err == unix.EINTR:
			continue
		case err != nil:
			return fmt.Errorf("sandbox: waiting for a worker command's namespace: %w", err)
		case k == 0:
			return fmt.Errorf("sandbox: processes of a worker command still running after %s", timeout)
		}
		return nil
	}
}

func signaled(s *os.ProcessState) bool {
	ws, ok := s.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled()
}

// drain is execenv.Wrapped.Drain.
func (n *nsWatch) drain(state *os.ProcessState) error {
	err := n.stopped(state, n.m.nsWaitTimeout())
	if err == nil {
		n.ok.Store(true)
	}
	return err
}

// finish releases the pipe and, when the namespace is confirmed empty, the
// pidfd and the registration. A watch that could not confirm stays
// registered, so Shutdown checks it again before the worktree is removed.
func (n *nsWatch) finish() {
	_ = n.r.Close()
	_ = n.w.Close()
	if n.ok.Load() || !n.started.Load() {
		n.release()
	}
}

func (n *nsWatch) release() {
	n.mu.Lock()
	if n.pidfd >= 0 {
		_ = unix.Close(n.pidfd)
		n.pidfd = -1
	}
	n.mu.Unlock()
	n.m.mu.Lock()
	delete(n.m.nsWatches, n)
	n.m.mu.Unlock()
}

// shutdownWatches waits for every namespace still registered.
func (m *Manager) shutdownWatches() error {
	m.mu.Lock()
	var left []*nsWatch
	for n := range m.nsWatches {
		left = append(left, n)
	}
	m.mu.Unlock()
	var errs []error
	for _, n := range left {
		if err := n.stopped(nil, m.nsWaitTimeout()); err != nil {
			errs = append(errs, err)
			continue
		}
		n.release()
	}
	return errors.Join(errs...)
}

// probeNSWatch fails when this host cannot give worker commands a watched
// namespace: no pidfd_open (kernel before 5.3), or a bwrap without
// --info-fd. Worker shell is then refused rather than torn down blindly.
func (m *Manager) probeNSWatch() error {
	if _, err := os.Stat("/proc/self/ns/pid"); err != nil {
		return fmt.Errorf("sandbox: PID namespaces unavailable: %w", err)
	}
	n, err := m.newNSWatch()
	if err != nil {
		return err
	}
	cmd := exec.Command(m.bwrap, "--ro-bind", "/", "/", "--dev", "/dev", "--unshare-net", "--unshare-pid", "--proc", "/proc", "--info-fd", "3", "--", "/bin/sh", "-c", "true")
	cmd.ExtraFiles = []*os.File{n.w}
	if err := cmd.Start(); err != nil {
		n.finish()
		return fmt.Errorf("sandbox: namespace probe: %w", err)
	}
	_ = n.w.Close()
	n.start()
	_ = cmd.Wait()
	err = n.stopped(cmd.ProcessState, m.nsWaitTimeout())
	n.mu.Lock()
	reported := n.pidfd >= 0 || n.gone
	n.mu.Unlock()
	if err == nil && !reported {
		err = errors.New("bwrap did not report its namespace (--info-fd unsupported?)")
	}
	n.ok.Store(err == nil)
	n.finish()
	if err != nil {
		return fmt.Errorf("sandbox: worker shell needs pidfd and bwrap --info-fd to confirm teardown: %w", err)
	}
	return nil
}

// attachNSWatch makes w report and wait for its PID namespace: bwrap gets
// --info-fd 3 (Plan.InfoFD), and Exec calls Started, Drain and Cleanup.
func (m *Manager) attachNSWatch(w *execenv.Wrapped) error {
	n, err := m.newNSWatch()
	if err != nil {
		return err
	}
	w.ExtraFiles = []*os.File{n.w}
	w.Started = n.start
	w.Drain = n.drain
	prev := w.Cleanup
	w.Cleanup = func() {
		if prev != nil {
			prev()
		}
		n.finish()
	}
	return nil
}
