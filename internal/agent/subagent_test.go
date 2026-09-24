package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/claude/agents"
)

var (
	small = budget.TierForWindow(32_768)
	large = budget.TierForWindow(200_000)
)

func TestAllowedToolNames(t *testing.T) {
	available := []string{"bash", "read", "edit", "write", "task", "mcp__x__y"}

	t.Run("matches Claude Code's casing against this harness's lowercase tools", func(t *testing.T) {
		got := AllowedToolNames([]string{"Read", "Bash"}, available)
		want := []string{"bash", "read"}
		if !equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("never grants task, so a subagent cannot dispatch further", func(t *testing.T) {
		if contains(AllowedToolNames(nil, available), "task") {
			t.Fatal("task leaked into an inherited allowlist")
		}
		if contains(AllowedToolNames([]string{"task", "Read"}, available), "task") {
			t.Fatal("task leaked into an explicit allowlist")
		}
	})

	t.Run("inherits everything when no allowlist is given", func(t *testing.T) {
		got := AllowedToolNames(nil, available)
		want := []string{"bash", "read", "edit", "write", "mcp__x__y"}
		if !equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("falls back to everything when an allowlist matches nothing", func(t *testing.T) {
		got := AllowedToolNames([]string{"Glob", "WebFetch"}, available)
		want := []string{"bash", "read", "edit", "write", "mcp__x__y"}
		if !equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
}

func manyAgents(n int) []agents.Definition {
	out := make([]agents.Definition, n)
	for i := range out {
		out[i] = agents.Definition{
			Name:        fmt.Sprintf("agent-%d", i),
			Description: strings.Repeat("x", 400),
		}
	}
	return out
}

func TestDescribeAgents(t *testing.T) {
	many := manyAgents(13)

	t.Run("clips descriptions on the small tier to protect the tool budget", func(t *testing.T) {
		got := DescribeAgents(many, small)
		wantLarge := DescribeAgents(many, large)
		if len(got) >= len(wantLarge) {
			t.Fatal("small tier was not cheaper than large")
		}
		if !strings.Contains(got, "...") {
			t.Fatal("nothing was clipped")
		}
		for _, a := range many {
			if !strings.Contains(got, a.Name) {
				t.Fatalf("%s missing from clipped catalog", a.Name)
			}
		}
	})

	t.Run("keeps full descriptions when the window can afford them", func(t *testing.T) {
		if strings.Contains(DescribeAgents(many, large), "...") {
			t.Fatal("large tier clipped when it should not have")
		}
	})

	t.Run("returns nothing for an empty roster", func(t *testing.T) {
		if got := DescribeAgents(nil, small); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
