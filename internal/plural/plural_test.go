package plural

import "testing"

func TestCount(t *testing.T) {
	for n, want := range map[int]string{0: "0 tools", 1: "1 tool", 3: "3 tools"} {
		if got := Count(n, "tool"); got != want {
			t.Errorf("Count(%d) = %q, want %q", n, got, want)
		}
	}
}
