package todo

import "testing"

func TestAdd(t *testing.T) {
	var l List
	l.Add("buy milk")
	l.Add("walk dog")
	items := l.Items()
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].Text != "buy milk" || items[0].Done {
		t.Fatalf("unexpected first item: %+v", items[0])
	}
}
