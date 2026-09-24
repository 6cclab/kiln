package agent

import "testing"

func TestTodoStoreSetNotifiesListeners(t *testing.T) {
	store := NewTodoStore()
	var got []TodoItem
	store.OnChange(func(items []TodoItem) { got = items })

	items := []TodoItem{{Content: "a", Status: TodoPending}}
	store.Set(items)

	if len(got) != 1 || got[0].Content != "a" {
		t.Fatalf("got %+v", got)
	}
	if len(store.Get()) != 1 {
		t.Fatalf("got %+v", store.Get())
	}
}

func TestTodoStoreSummary(t *testing.T) {
	store := NewTodoStore()
	if store.Summary() != "no todos" {
		t.Fatalf("got %q", store.Summary())
	}
	store.Set([]TodoItem{
		{Content: "a", Status: TodoCompleted},
		{Content: "b", Status: TodoPending},
	})
	if store.Summary() != "1/2 done" {
		t.Fatalf("got %q", store.Summary())
	}
}
