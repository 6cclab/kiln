package skills

import (
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/claude/paths"
)

// TestOrderForIndex: project and local skills come first, user skills
// next, plugin skills last, each group keeping its input order, and the
// input slice is left untouched.
func TestOrderForIndex(t *testing.T) {
	in := []Skill{
		{Name: "plug-a", Scope: paths.ScopePlugin},
		{Name: "user-a", Scope: paths.ScopeUser},
		{Name: "proj-a", Scope: paths.ScopeProject},
		{Name: "plug-b", Scope: paths.ScopePlugin},
		{Name: "local-a", Scope: paths.ScopeLocal},
		{Name: "user-b", Scope: paths.ScopeUser},
		{Name: "proj-b", Scope: paths.ScopeProject},
	}
	orig := append([]Skill(nil), in...)

	got := OrderForIndex(in)
	var names []string
	for _, s := range got {
		names = append(names, s.Name)
	}
	want := "proj-a local-a proj-b user-a user-b plug-a plug-b"
	if strings.Join(names, " ") != want {
		t.Errorf("OrderForIndex = %s, want %s", strings.Join(names, " "), want)
	}
	for i := range in {
		if in[i].Name != orig[i].Name {
			t.Fatalf("OrderForIndex reordered its input slice")
		}
	}
	if got := OrderForIndex(nil); len(got) != 0 {
		t.Errorf("OrderForIndex(nil) = %v, want empty", got)
	}
}
