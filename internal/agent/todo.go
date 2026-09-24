package agent

import (
	"fmt"
	"sync"

	"github.com/andrepato/harness/internal/tools"
)

// TodoStatus is one todo item's lifecycle state. Alias of tools.TodoStatus:
// the todo_write tool (internal/tools/todo.go) owns the wire type, since it
// is the one that decodes the model's JSON arguments; the store here just
// holds what that tool produced.
type TodoStatus = tools.TodoStatus

const (
	TodoPending    = tools.TodoPending
	TodoInProgress = tools.TodoInProgress
	TodoCompleted  = tools.TodoCompleted
)

// TodoItem is one row of the todo list, as the model reports it and the
// TUI/print-mode renders it.
type TodoItem = tools.TodoItem

// TodoStore is the todo list: session state, not conversation state. It
// lives here rather than accumulating in the transcript, matching
// src/agent/todo.ts's own reasoning — on a 32k window, re-sending the full
// list on every update would spend the context the list exists to help
// manage.
type TodoStore struct {
	mu        sync.Mutex
	items     []TodoItem
	listeners []func([]TodoItem)
}

// NewTodoStore returns an empty store.
func NewTodoStore() *TodoStore { return &TodoStore{} }

// Get returns the current list.
func (s *TodoStore) Get() []TodoItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.items
}

// Set replaces the whole list and notifies every listener.
func (s *TodoStore) Set(items []TodoItem) {
	s.mu.Lock()
	s.items = items
	listeners := append([]func([]TodoItem){}, s.listeners...)
	s.mu.Unlock()
	for _, l := range listeners {
		l(items)
	}
}

// OnChange registers a listener called after every Set.
func (s *TodoStore) OnChange(listener func([]TodoItem)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listeners = append(s.listeners, listener)
}

// Summary is a one-line summary for the footer or a status command.
func (s *TodoStore) Summary() string {
	items := s.Get()
	if len(items) == 0 {
		return "no todos"
	}
	done := 0
	for _, t := range items {
		if t.Status == TodoCompleted {
			done++
		}
	}
	return fmt.Sprintf("%d/%d done", done, len(items))
}
