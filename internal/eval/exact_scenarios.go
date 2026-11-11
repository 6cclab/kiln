package eval

import (
	"fmt"

	"github.com/andrepato/harness/internal/testkit/scenario"
)

// exactScenarios preserves requested order, with no substring or tag matches.
func exactScenarios(all []*scenario.Scenario, names []string) ([]*scenario.Scenario, error) {
	byName := make(map[string]*scenario.Scenario, len(all))
	for _, sc := range all {
		if byName[sc.Name] != nil {
			return nil, fmt.Errorf("eval: duplicate scenario name %q", sc.Name)
		}
		byName[sc.Name] = sc
	}
	seen := make(map[string]bool, len(names))
	selected := make([]*scenario.Scenario, 0, len(names))
	for _, name := range names {
		if seen[name] {
			return nil, fmt.Errorf("eval: repeated exact scenario name %q", name)
		}
		seen[name] = true
		sc := byName[name]
		if sc == nil {
			return nil, fmt.Errorf("eval: exact scenario %q not found", name)
		}
		selected = append(selected, sc)
	}
	return selected, nil
}
