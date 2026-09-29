package greeter

import "testing"

func TestGreet(t *testing.T) {
	if got, want := Greet("Ada"), "Hello, Ada!"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
