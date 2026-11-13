package agent

import (
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/claude/agents"
)

// TestAddAgentExtendsTheListAndRefusesShadowing: a registered agent joins
// the list Dispatch resolves against, after the catalog; a name the catalog
// or an earlier registration already has is refused, so a mod cannot
// shadow a built-in or a user's agent.
func TestAddAgentExtendsTheListAndRefusesShadowing(t *testing.T) {
	d := &Dispatcher{Agents: []agents.Definition{{Name: "general-purpose"}}}
	if got := d.AgentList(); len(got) != 1 || got[0].Name != "general-purpose" {
		t.Fatalf("catalog only: %+v", got)
	}

	if err := d.AddAgent(agents.Definition{Name: "reviewer"}); err != nil {
		t.Fatalf("AddAgent(reviewer): %v", err)
	}
	got := d.AgentList()
	if len(got) != 2 || got[0].Name != "general-purpose" || got[1].Name != "reviewer" {
		t.Fatalf("after AddAgent: %+v", got)
	}
	if def, ok := findAgentDefinition(got, "reviewer"); !ok || def.Name != "reviewer" {
		t.Fatal("Dispatch could not resolve the registered agent")
	}

	for _, name := range []string{"general-purpose", "reviewer"} {
		if err := d.AddAgent(agents.Definition{Name: name}); err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Errorf("AddAgent(%s) shadowed an existing agent: err=%v", name, err)
		}
	}
	if n := len(d.AgentList()); n != 2 {
		t.Fatalf("a refused AddAgent changed the list: %d entries", n)
	}
	// The catalog slice itself is never appended to.
	if len(d.Agents) != 1 {
		t.Fatalf("AddAgent wrote into the catalog: %+v", d.Agents)
	}
}
