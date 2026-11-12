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

func TestTodoStoreOwnsSnapshots(t *testing.T) {
	store := NewTodoStore()
	store.OnChange(func(items []TodoItem) { items[0].Content = "listener mutation" })
	items := []TodoItem{{Content: "saved", Status: TodoPending}}
	store.Set(items)
	items[0].Content = "caller mutation"
	got := store.Get()
	got[0].Content = "snapshot mutation"
	if store.Get()[0].Content != "saved" {
		t.Fatal("shared mutable todo slice")
	}
}
