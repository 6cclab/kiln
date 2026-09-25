package tool

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/andrepato/harness/internal/msg"
)

func newT(name string) *Tool {
	return &Tool{
		Name: name,
		Execute: func(ctx context.Context, args json.RawMessage, onUpdate Update, inv Invocation) (Result, error) {
			return Result{}, nil
		},
	}
}

func TestSetAddReplaceKeepsOrder(t *testing.T) {
	s := NewSet(newT("a"), newT("b"), newT("c"))
	// Replace "b" with a new tool instance; order must be unchanged.
	replacement := newT("b")
	replacement.Label = "replaced"
	s.Add(replacement)

	want := []string{"a", "b", "c"}
	if got := s.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}

	got, ok := s.Get("b")
	if !ok {
		t.Fatalf("Get(b) ok=false, want true")
	}
	if got.Label != "replaced" {
		t.Fatalf("Get(b).Label = %q, want %q", got.Label, "replaced")
	}
}

func TestSetGetMiss(t *testing.T) {
	s := NewSet(newT("a"))
	if _, ok := s.Get("missing"); ok {
		t.Fatalf("Get(missing) ok=true, want false")
	}
}

func TestSetNamesInsertionOrder(t *testing.T) {
	s := NewSet(newT("z"), newT("a"), newT("m"))
	want := []string{"z", "a", "m"}
	if got := s.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
}

func TestSetNamesReturnsCopy(t *testing.T) {
	s := NewSet(newT("a"))
	names := s.Names()
	names[0] = "mutated"
	if s.Names()[0] != "a" {
		t.Fatalf("Names() mutation leaked into set: got %v", s.Names())
	}
}

func TestSetSelectSubsetInOrderIgnoresUnknown(t *testing.T) {
	s := NewSet(newT("a"), newT("b"), newT("c"), newT("d"))
	got := s.Select([]string{"d", "unknown", "a"})
	if len(got) != 2 {
		t.Fatalf("Select() returned %d tools, want 2: %+v", len(got), got)
	}
	// Result order follows the set's order, not the requested order.
	if got[0].Name != "a" || got[1].Name != "d" {
		t.Fatalf("Select() = [%s, %s], want [a, d]", got[0].Name, got[1].Name)
	}
}

func TestSetSelectEmpty(t *testing.T) {
	s := NewSet(newT("a"), newT("b"))
	got := s.Select(nil)
	if len(got) != 0 {
		t.Fatalf("Select(nil) = %v, want empty", got)
	}
	got = s.Select([]string{"unknown"})
	if len(got) != 0 {
		t.Fatalf("Select(unknown) = %v, want empty", got)
	}
}

func TestTextResult(t *testing.T) {
	r := Text("hello")
	if r.IsError {
		t.Fatalf("Text().IsError = true, want false")
	}
	if len(r.Content) != 1 {
		t.Fatalf("Text() Content len = %d, want 1", len(r.Content))
	}
	tc, ok := r.Content[0].(msg.TextContent)
	if !ok {
		t.Fatalf("Text() Content[0] = %T, want msg.TextContent", r.Content[0])
	}
	if tc.Text != "hello" {
		t.Fatalf("Text() Content[0].Text = %q, want %q", tc.Text, "hello")
	}
}

func TestErrorfResult(t *testing.T) {
	r := Errorf("bad thing: %d", 42)
	if !r.IsError {
		t.Fatalf("Errorf().IsError = false, want true")
	}
	if len(r.Content) != 1 {
		t.Fatalf("Errorf() Content len = %d, want 1", len(r.Content))
	}
	tc, ok := r.Content[0].(msg.TextContent)
	if !ok {
		t.Fatalf("Errorf() Content[0] = %T, want msg.TextContent", r.Content[0])
	}
	want := "bad thing: 42"
	if tc.Text != want {
		t.Fatalf("Errorf() Content[0].Text = %q, want %q", tc.Text, want)
	}
}
