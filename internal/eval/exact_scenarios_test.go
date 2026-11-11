package eval

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/testkit/scenario"
)

func TestExactScenarios(t *testing.T) {
	all := []*scenario.Scenario{{Name: "check"}, {Name: "check-extra"}, {Name: "other", Tags: []string{"check"}}}
	for _, tc := range []struct {
		name      string
		names     []string
		wantError bool
	}{
		{"exact", []string{"check"}, false}, {"missing", []string{"missing"}, true}, {"duplicate", []string{"check", "check"}, true}, {"tag is not name", []string{"extra"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := exactScenarios(all, tc.names)
			if (err != nil) != tc.wantError {
				t.Fatalf("got %v, %v", got, err)
			}
			if !tc.wantError && (len(got) != 1 || got[0].Name != "check") {
				t.Fatalf("unexpected selection: %+v", got)
			}
		})
	}
	if _, err := exactScenarios(append(all, &scenario.Scenario{Name: "check"}), []string{"check"}); err == nil {
		t.Fatal("duplicate catalog name accepted")
	}
}

func TestRunRefusesLivePinnedModelBeforeExecution(t *testing.T) {
	t.Setenv("KILN_EVAL_LIVE", "")
	scenarios, fixtures := setupTinyScenario(t)
	writeFile(t, filepath.Join(scenarios, "tiny-fix", "scenario.yaml"), strings.Replace(tinyScenarioYAML, "tags: [smoke]", "tags: [pin-model:review-test/no-such-model]", 1))
	result, err := Run(context.Background(), RunOptions{KilnBin: kilnBin, ScenariosDir: scenarios, FixturesDir: fixtures, ResultsDir: t.TempDir(), Models: []ModelConfig{{Provider: "faux", Model: "faux-1", Faux: true}}})
	if err == nil || !strings.Contains(err.Error(), "live model(s) refused") || len(result.Records) != 0 {
		t.Fatalf("pinned model bypassed live gate: %+v, %v", result, err)
	}
}
