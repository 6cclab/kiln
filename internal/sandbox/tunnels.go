package sandbox

import (
	"io"
	"sync"
)

// tunnels tracks the connections no server owns any more: CONNECT tunnels
// the proxy hijacked from its http.Server, and the bridge listeners and
// relays of the Linux sandbox. http.Server.Close skips hijacked
// connections and closing a listener leaves accepted ones open, so without
// this a tunnel outlived Close until one of its far ends hung up.
//
// Each tunnel is one goroutine that calls begin before its copying starts
// and end when it has finished. closeAll closes every tracked closer and
// waits for those goroutines, so when it returns none is left running.
type tunnels struct {
	mu      sync.Mutex
	closers map[io.Closer]struct{}
	closed  bool
	wg      sync.WaitGroup
}

// begin starts tracking a tunnel and the closers it owns. It reports false,
// having closed them, when closeAll has already run; the caller must not
// call end then.
func (t *tunnels) begin(cs ...io.Closer) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		closeEach(cs)
		return false
	}
	t.trackLocked(cs)
	t.wg.Add(1)
	return true
}

// track adds closers to a tunnel that has begun. It reports false, having
// closed them, when closeAll has already run.
func (t *tunnels) track(cs ...io.Closer) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		closeEach(cs)
		return false
	}
	t.trackLocked(cs)
	return true
}

func (t *tunnels) trackLocked(cs []io.Closer) {
	if t.closers == nil {
		t.closers = map[io.Closer]struct{}{}
	}
	for _, c := range cs {
		t.closers[c] = struct{}{}
	}
}

// end closes and forgets a tunnel's closers and marks its goroutine done.
func (t *tunnels) end(cs ...io.Closer) {
	t.mu.Lock()
	for _, c := range cs {
		delete(t.closers, c)
	}
	t.mu.Unlock()
	closeEach(cs)
	t.wg.Done()
}

// closeAll closes every tracked closer, refuses new tunnels, and waits for
// the running ones to end.
func (t *tunnels) closeAll() {
	t.mu.Lock()
	t.closed = true
	cs := make([]io.Closer, 0, len(t.closers))
	for c := range t.closers {
		cs = append(cs, c)
	}
	t.closers = nil
	t.mu.Unlock()
	closeEach(cs)
	t.wg.Wait()
}

func closeEach(cs []io.Closer) {
	for _, c := range cs {
		_ = c.Close()
	}
}
