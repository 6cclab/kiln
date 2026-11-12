//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

func TestContextGoldenMasksPathSensitivePercentages(t *testing.T) {
	for _, label := range []string{"System prompt", "Conversation"} {
		shortPath := "■ " + label + "       514    <1%  "
		longPath := "■ " + label + "       628     1%  "
		a, okA := maskCwdTokens(shortPath)
		b, okB := maskCwdTokens(longPath)
		if !okA || !okB || a != b {
			t.Fatalf("%s path-sensitive rows normalize differently: %q (%v), %q (%v)", label, a, okA, b, okB)
		}
		if !strings.HasSuffix(a, "N      P  ") {
			t.Fatalf("%s normalization lost right-aligned columns: %q", label, a)
		}
	}
	for _, row := range []string{"■ Tools               3.6k    3%", "■ Free                123k   96%"} {
		if got, changed := maskCwdTokens(row); changed || got != row {
			t.Fatalf("stable row changed: %q -> %q", row, got)
		}
	}
}
