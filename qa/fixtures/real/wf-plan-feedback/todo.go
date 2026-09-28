// Package todo is a tiny in-memory todo list used to exercise kiln's plan
// mode against a real, small change request.
package todo

import "errors"

// Item is a single todo entry.
type Item struct {
	Text string
	Done bool
}

// List holds todo items in the order they were added.
type List struct {
	items []Item
}

// Add appends a new, not-done item.
func (l *List) Add(text string) {
	l.items = append(l.items, Item{Text: text})
}

// Items returns the items in insertion order.
func (l *List) Items() []Item {
	return l.items
}

// ErrOutOfRange is returned when an index does not name an existing item.
var ErrOutOfRange = errors.New("todo: index out of range")

func (l *List) checkIndex(i int) error {
	if i < 0 || i >= len(l.items) {
		return ErrOutOfRange
	}
	return nil
}
