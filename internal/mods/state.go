package mods

import "sync"

// stateKey names one $.state value: a plugin and a key, as atom() names it.
type stateKey struct{ plugin, key string }

// stateStore holds $.state: the session's reactive values, each kept as its
// JSON text. Every read returns a fresh copy (JSON.parse of the text), as a
// value crossing Claude Code's worker boundary would be, so a mod mutating
// what it read changes nothing. A write that changes the text of a value a
// ui.render hook has read asks the host to redraw.
//
// Only the worker goroutine reads or writes values; mu guards snapshot reads
// from tests and kiln.
type stateStore struct {
	mu         sync.Mutex
	values     map[stateKey]string
	renderRead map[stateKey]bool
}

func newStateStore() *stateStore {
	return &stateStore{values: map[stateKey]string{}, renderRead: map[stateKey]bool{}}
}

// get returns the value's JSON text, or false when it was never set.
func (s *stateStore) get(k stateKey, fromRender bool) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fromRender {
		s.renderRead[k] = true
	}
	v, ok := s.values[k]
	return v, ok
}

// set stores the value's JSON text (ok false deletes it) and reports whether
// a drawing that read it needs redrawing.
func (s *stateStore) set(k stateKey, text string, ok bool) (redraw bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, had := s.values[k]
	if ok {
		if had && old == text {
			return false
		}
		s.values[k] = text
	} else {
		if !had {
			return false
		}
		delete(s.values, k)
	}
	return s.renderRead[k]
}

// snapshot returns a value's JSON text, for tests and diagnostics.
func (s *stateStore) snapshot(plugin, key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[stateKey{plugin, key}]
	return v, ok
}
