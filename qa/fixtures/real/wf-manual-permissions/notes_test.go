package notes

import "testing"

func TestCount(t *testing.T) {
	if got, want := Count([]string{"a", "b"}), 2; got != want {
		t.Fatalf("got %d, want %d", got, want)
	}
}
