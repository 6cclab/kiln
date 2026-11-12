package faux

import (
	"context"
	"net/http"
	"sync"
)

// holdState is one named hold. Its channel is closed on release, which
// frees every request waiting at the hold now and lets later ones through.
type holdState struct {
	ch      chan struct{}
	waiting int
	open    bool
}

// holds is the registry behind the hold script step and POST
// /_faux/release. A release is latched: it is remembered under the name, so
// releasing before the request reaches the hold (or a name no script uses)
// is not an error and the hold is simply open when reached. It stays open
// until /_faux/reset or a script load, which forget released names but
// leave holds that requests are still waiting on.
type holds struct {
	mu sync.Mutex
	m  map[string]*holdState
}

func newHolds() *holds { return &holds{m: map[string]*holdState{}} }

// get returns the named hold, creating it closed. Callers hold h.mu.
func (h *holds) get(name string) *holdState {
	st := h.m[name]
	if st == nil {
		st = &holdState{ch: make(chan struct{})}
		h.m[name] = st
	}
	return st
}

// wait blocks until the named hold is released, returning false when ctx
// ends first (the client hung up).
func (h *holds) wait(ctx context.Context, name string) bool {
	h.mu.Lock()
	st := h.get(name)
	if st.open {
		h.mu.Unlock()
		return true
	}
	st.waiting++
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		st.waiting--
		h.mu.Unlock()
	}()
	select {
	case <-st.ch:
		return true
	case <-ctx.Done():
		return false
	}
}

// release opens the named hold and returns how many requests were waiting
// at it (and are now freed).
func (h *holds) release(name string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.get(name)
	if !st.open {
		st.open = true
		close(st.ch)
	}
	return st.waiting
}

// forget drops released holds nobody is waiting on (see the type comment).
func (h *holds) forget() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for name, st := range h.m {
		if st.open && st.waiting == 0 {
			delete(h.m, name)
		}
	}
}

// waitingCounts reports, per hold name, how many requests are held now.
func (h *holds) waitingCounts() map[string]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]int{}
	for name, st := range h.m {
		if st.waiting > 0 {
			out[name] = st.waiting
		}
	}
	return out
}

// waitAllHolds waits for every hold in a turn, in order. Non-streaming
// responses are written in one piece, so their holds all sit before the
// first byte.
func (s *Server) waitAllHolds(ctx context.Context, t turn) bool {
	for _, c := range t.content {
		if c.hold != "" && !s.holds.wait(ctx, c.hold) {
			return false
		}
	}
	return true
}

// Release releases the named hold (see the Holds section of the package
// documentation) and returns how many waiting requests it freed.
func (s *Server) Release(name string) int { return s.holds.release(name) }

// Held reports how many requests are waiting at the named hold now.
func (s *Server) Held(name string) int {
	return s.holds.waitingCounts()[name]
}

func (s *Server) handleFauxRelease(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "release: missing ?name="})
		return
	}
	freed := s.Release(name)
	writeJSON(w, http.StatusOK, map[string]any{"status": "released", "name": name, "freed": freed})
}
