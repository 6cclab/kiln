package agent

import (
	"encoding/json"
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
		got := AllowedToolNames([]string{"Read", "Bash"}, available, true)
		want := []string{"bash", "read"}
		if !equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("strips task when allowTask is false, so a depth-limited subagent cannot dispatch further", func(t *testing.T) {
		if contains(AllowedToolNames(nil, available, false), "task") {
			t.Fatal("task leaked into an inherited allowlist with allowTask=false")
		}
		if contains(AllowedToolNames([]string{"task", "Read"}, available, false), "task") {
			t.Fatal("task leaked into an explicit allowlist with allowTask=false")
		}
	})

	t.Run("keeps task when allowTask is true and it is requested or unrestricted", func(t *testing.T) {
		if !contains(AllowedToolNames(nil, available, true), "task") {
			t.Fatal("task missing from an inherited allowlist with allowTask=true")
		}
		if !contains(AllowedToolNames([]string{"task", "Read"}, available, true), "task") {
			t.Fatal("task missing from an explicit allowlist that names it, with allowTask=true")
		}
	})

	t.Run("inherits everything when no allowlist is given", func(t *testing.T) {
		got := AllowedToolNames(nil, available, false)
		want := []string{"bash", "read", "edit", "write", "mcp__x__y"}
		if !equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("falls back to everything when an allowlist matches nothing", func(t *testing.T) {
		got := AllowedToolNames([]string{"Glob", "WebFetch"}, available, false)
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

func TestTaskParametersOmitsModelWithNoRoles(t *testing.T) {
	raw := TaskParameters([]agents.Definition{GeneralPurpose}, nil)
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decoding schema: %v", err)
	}
	if _, ok := schema.Properties["model"]; ok {
		t.Fatal("model property present with no roles configured")
	}
}

func TestTaskParametersModelEnumWithRoles(t *testing.T) {
	roles := map[string]string{"heavy": "anthropic/claude", "fast": "faux/faux-2"}
	raw := TaskParameters([]agents.Definition{GeneralPurpose}, roles)
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decoding schema: %v", err)
	}
	model, ok := schema.Properties["model"]
	if !ok {
		t.Fatal("model property missing with roles configured")
	}
	want := []string{"fast", "heavy", "inherit"}
	if len(model.Enum) != len(want) {
		t.Fatalf("enum = %v, want %v", model.Enum, want)
	}
	for i := range want {
		if model.Enum[i] != want[i] {
			t.Fatalf("enum = %v, want %v", model.Enum, want)
		}
	}
}

func TestTaskDescriptionListsRolesOnlyWhenConfigured(t *testing.T) {
	withRoles := TaskDescription([]agents.Definition{GeneralPurpose}, map[string]string{"fast": "faux/faux-2"}, large)
	if !strings.Contains(withRoles, "fast: faux/faux-2") {
		t.Fatalf("description = %q, want it to list the role", withRoles)
	}

	without := TaskDescription([]agents.Definition{GeneralPurpose}, nil, large)
	if strings.Contains(without, "Available model roles") {
		t.Fatal("description advertises roles with none configured")
	}
}

func TestDescribeRolesClipsOnSmallTier(t *testing.T) {
	roles := map[string]string{"heavy": "provider-" + strings.Repeat("x", 400) + "/model"}
	got := DescribeRoles(roles, small)
	wantLarge := DescribeRoles(roles, large)
	if len(got) >= len(wantLarge) {
		t.Fatal("small tier was not cheaper than large")
	}
	if !strings.Contains(got, "...") {
		t.Fatal("nothing was clipped")
	}
	if got := DescribeRoles(nil, small); got != "" {
		t.Fatalf("got %q, want empty for no roles", got)
	}
}
