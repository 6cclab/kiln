package agent

import (
	"fmt"

	"github.com/andrepato/harness/internal/claude/agents"
)

// AddAgent makes def dispatchable by name from now on (kiln's mod engine:
// $.agent.register). It errors when a definition of that name exists, so a
// mod cannot shadow a built-in or a user's agent.
//
// The task tool's own listing of agent types is built once at startup and
// does not include it: the model is not told of it, but a mod's $.agent.spawn
// and a task call naming it reach it.
func (d *Dispatcher) AddAgent(def agents.Definition) error {
	d.agentsMu.Lock()
	defer d.agentsMu.Unlock()
	if _, ok := findAgentDefinition(d.Agents, def.Name); ok {
		return fmt.Errorf("agent: %q already exists", def.Name)
	}
	if _, ok := findAgentDefinition(d.extraAgents, def.Name); ok {
		return fmt.Errorf("agent: %q already exists", def.Name)
	}
	d.extraAgents = append(d.extraAgents, def)
	return nil
}

// AgentList is every definition Dispatch resolves a name against: the
// catalog, then the ones AddAgent made.
func (d *Dispatcher) AgentList() []agents.Definition {
	d.agentsMu.RLock()
	defer d.agentsMu.RUnlock()
	if len(d.extraAgents) == 0 {
		return d.Agents
	}
	out := make([]agents.Definition, 0, len(d.Agents)+len(d.extraAgents))
	out = append(out, d.Agents...)
	return append(out, d.extraAgents...)
}
