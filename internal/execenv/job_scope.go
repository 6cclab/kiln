package execenv

import (
	"errors"
	"os/exec"
	"sync"
	"time"
)

// ErrJobScopeClosed means a command could not start because its job scope
// was closed, or was stopped when the scope closed.
var ErrJobScopeClosed = errors.New("execenv: job scope closed")

// JobScope owns the process groups started by one Env. A worker gives its
// Env a fresh scope and closes it before inspecting or removing its files.
// On Unix this stops background descendants that remain in the process
// group after the shell exits. It cannot reap processes it did not start.
// Windows process-tree termination is weaker once a child is reparented.
// The zero value is ready for use; NewJobScope is the usual constructor.
type JobScope struct {
	mu     sync.Mutex
	closed bool
	active int
	done   chan struct{}
	killed chan struct{}
	groups map[int]*scopedGroup
}

type scopedGroup struct {
	stopped bool
}

// NewJobScope returns an independent scope for one worker's commands.
func NewJobScope() *JobScope {
	return &JobScope{done: make(chan struct{}), groups: make(map[int]*scopedGroup)}
}

func (s *JobScope) initLocked() {
	if s.done == nil {
		s.done = make(chan struct{})
	}
	if s.groups == nil {
		s.groups = make(map[int]*scopedGroup)
	}
}

// start serializes process creation and registration with Close. In
// particular, Close cannot observe an empty scope while Start is about to
// launch a child.
func (s *JobScope) start(cmd *exec.Cmd) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initLocked()
	if s.closed {
		return ErrJobScopeClosed
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	s.groups[cmd.Process.Pid] = &scopedGroup{}
	s.active++
	return nil
}

// processFinished drops an exited command's group if it has no surviving
// descendants. Groups with background jobs remain owned until Close.
func (s *JobScope) processFinished(pid int, jobsLeft bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.groups[pid]
	if !jobsLeft {
		delete(s.groups, pid)
	}
	return g != nil && g.stopped
}

func (s *JobScope) execFinished() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active--
	if s.closed && s.active == 0 {
		close(s.done)
	}
}

// Close stops every group this scope started, including active commands
// and background descendants. It is safe to call concurrently or more
// than once. It waits at most three seconds for active Exec calls to finish;
// a caller-provided output callback can delay their return beyond that.
func (s *JobScope) Close() {
	if s == nil {
		return
	}
	deadline := time.Now().Add(3 * time.Second)
	s.mu.Lock()
	s.initLocked()
	var groups []int
	firstClose := !s.closed
	if !s.closed {
		s.closed = true
		s.killed = make(chan struct{})
		for pid, g := range s.groups {
			g.stopped = true
			groups = append(groups, pid)
		}
		if s.active == 0 {
			close(s.done)
		}
	}
	killed := s.killed
	done := s.done
	s.mu.Unlock()
	if firstClose {
		for _, pid := range groups {
			_ = killScopedGroup(pid, deadline)
		}
		close(killed)
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-killed:
	case <-timer.C:
		return
	}
	select {
	case <-done:
	case <-timer.C:
	}
}
