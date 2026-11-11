package cli

import (
	"testing"

	"github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/learning"
)

func TestApplyLearnedRoutingReportsEvaluatedSourceForFalse(t *testing.T) {
	config := settings.ModelRouterConfig{ModelRoles: map[string]string{"fast": "provider/model"}, Conservative: true}
	active := &learning.Active{Policy: learning.Policy{SkillBudgetTokens: 512, ConservativeRouting: false}, Report: learning.Report{Roles: map[string]string{"fast": "provider/model"}, Models: []string{"kiln/auto"}}}
	applyLearnedRouting(&config, active)
	if config.Conservative || config.ConservativeSource != "evaluated personal policy" {
		t.Fatalf("learned false not attributed: %+v", config)
	}
}
