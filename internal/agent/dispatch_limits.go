package agent

import (
	"fmt"
	"sync"
	"time"
)

// DispatchLimits is shared by every dispatcher in a session tree. Saturation
// refuses new work rather than blocking: a waiting parent holding a worker slot
// must never deadlock a nested worker trying to acquire the same semaphore.
type DispatchLimits struct {
	mu                            sync.Mutex
	active, dispatched            int
	Concurrent, Dispatches, Depth int
	Timeout                       time.Duration
}

func (l *DispatchLimits) acquire(depth int) (func(), error) {
	if l == nil {
		return func() {}, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.Depth > 0 && depth > l.Depth {
		return nil, fmt.Errorf("subagent depth limit %d reached", l.Depth)
	}
	if l.Dispatches > 0 && l.dispatched >= l.Dispatches {
		return nil, fmt.Errorf("session subagent dispatch limit %d reached", l.Dispatches)
	}
	if l.Concurrent > 0 && l.active >= l.Concurrent {
		return nil, fmt.Errorf("subagent concurrency limit %d reached; finish existing work before delegating again", l.Concurrent)
	}
	l.active++
	l.dispatched++
	var once sync.Once
	return func() { once.Do(func() { l.mu.Lock(); defer l.mu.Unlock(); l.active-- }) }, nil
}
