package learning

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/andrepato/harness/internal/diag"
	"github.com/andrepato/harness/internal/memory"
	"github.com/andrepato/harness/internal/testkit/scenario"
)

func readBoundedResults(path string) ([]Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer diag.Close(f)
	data, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 8<<20 {
		return nil, fmt.Errorf("evaluation results exceed 8 MiB")
	}
	return readResultsReader(bytes.NewReader(data))
}

func validateManifest(c Candidate, m Manifest) error {
	if m.CandidateID != c.ID || m.BaselinePolicy.Hash() != c.SourcePolicyHash || m.BaselinePolicyHash != c.SourcePolicyHash || m.CandidatePolicy.Hash() != c.Policy.Hash() || m.Model != c.SourceModel || m.BinaryHash != c.SourceBinaryHash || m.Version != c.SourceVersion || !SameRoles(m.Roles, c.SourceRoles) || len(m.Scenarios) != 5 || m.Repeats != 4 || m.Pairs != 20 || m.TotalRuns != 40 {
		return fmt.Errorf("plan provenance no longer matches candidate")
	}
	all, err := scenario.List(m.ScenarioDir)
	if err != nil {
		return err
	}
	byName := map[string]*scenario.Scenario{}
	for _, sc := range all {
		if byName[sc.Name] != nil {
			return fmt.Errorf("duplicate scenario name %q", sc.Name)
		}
		byName[sc.Name] = sc
	}
	sourceNames := map[string]bool{}
	sourceDatasets := map[string]bool{}
	sourceFixtures := map[string]bool{}
	for _, name := range c.SourceScenarioNames {
		sourceNames[name] = true
	}
	for _, hash := range c.SourceDatasets {
		sourceDatasets[hash] = true
	}
	for _, hash := range c.SourceFixtures {
		sourceFixtures[hash] = true
	}
	seen := map[string]bool{}
	if len(m.ExactNames) != len(m.Scenarios) {
		return fmt.Errorf("plan exact name list is incomplete")
	}
	for i, planned := range m.Scenarios {
		if m.ExactNames[i] != planned.Name {
			return fmt.Errorf("plan exact name list differs from scenarios")
		}
		if seen[planned.Name] || sourceNames[planned.Name] {
			return fmt.Errorf("plan repeats or overlaps source scenario")
		}
		seen[planned.Name] = true
		sc := byName[planned.Name]
		if sc == nil {
			return fmt.Errorf("planned scenario %q missing", planned.Name)
		}
		if err := validatePlannedScenarioMetadata(sc, c.SourceModel); err != nil {
			return err
		}
		dataset, fixture, err := scenarioFingerprint(sc, m.FixturesDir)
		if err != nil {
			return err
		}
		if dataset != planned.DatasetHash || fixture != planned.FixtureHash {
			return fmt.Errorf("planned scenario %q changed after planning", planned.Name)
		}
		if sourceDatasets[dataset] || sourceFixtures[fixture] {
			return fmt.Errorf("planned scenario %q overlaps source fixture", planned.Name)
		}
	}
	return nil
}

func validateCandidateRuns(c Candidate, m Manifest, baseline, candidate []Result) error {
	if len(baseline) != 20 || len(candidate) != 20 {
		return fmt.Errorf("planned evaluation requires exactly 20 runs per arm")
	}
	planned := map[string]PlannedScenario{}
	for _, s := range m.Scenarios {
		planned[s.Name] = s
	}
	sourceNames := map[string]bool{}
	sourceFixtures := map[string]bool{}
	for _, name := range c.SourceScenarioNames {
		sourceNames[name] = true
	}
	for _, hash := range c.SourceFixtures {
		sourceFixtures[hash] = true
	}
	checkArm := func(results []Result, policyHash string) (string, error) {
		runID := ""
		seen := map[string]bool{}
		for _, r := range results {
			sc, ok := planned[r.Scenario]
			if !ok || sourceNames[r.Scenario] || sourceFixtures[r.FixtureHash] || r.DatasetHash != sc.DatasetHash || r.FixtureHash != sc.FixtureHash || r.PolicyHash != policyHash || r.Provider+"/"+r.Model != c.SourceModel || r.BinaryHash != c.SourceBinaryHash || r.Version != c.SourceVersion || !SameRoles(r.Roles, c.SourceRoles) || !heldOut(r) || r.Repeat < 1 || r.Repeat > 4 {
				return "", fmt.Errorf("evaluation result does not match planned held-out scenario or provenance")
			}
			if runID == "" {
				runID = r.RunID
			}
			if r.RunID == "" || r.RunID == c.SourceRunID || r.RunID != runID {
				return "", fmt.Errorf("evaluation arm has invalid run ID")
			}
			k := fmt.Sprintf("%s\x00%d", r.Scenario, r.Repeat)
			if seen[k] {
				return "", fmt.Errorf("duplicate planned scenario repeat")
			}
			seen[k] = true
		}
		if len(seen) != 20 {
			return "", fmt.Errorf("evaluation arm is missing planned repeats")
		}
		return runID, nil
	}
	a, err := checkArm(baseline, c.SourcePolicyHash)
	if err != nil {
		return err
	}
	b, err := checkArm(candidate, c.Policy.Hash())
	if err != nil {
		return err
	}
	if a == b {
		return fmt.Errorf("baseline and candidate use the same run ID")
	}
	return nil
}

// PromoteCandidate revalidates a queued plan and paired results before writing
// the active policy. Result files are each read once; no evaluation is started.
func PromoteCandidate(ctx context.Context, store *memory.Store, project, id, baselinePath, candidatePath string) (Report, error) {
	var empty Report
	if strings.TrimSpace(baselinePath) == strings.TrimSpace(candidatePath) {
		return empty, fmt.Errorf("baseline and candidate paths must differ")
	}
	c, err := LoadCandidate(ctx, store, project, id)
	if err != nil {
		return empty, err
	}
	if c == nil {
		return empty, fmt.Errorf("candidate not found")
	}
	m, err := LoadManifest(ctx, store, project, id)
	if err != nil {
		return empty, err
	}
	if m == nil {
		return empty, fmt.Errorf("candidate has no validated plan")
	}
	if err := validateManifest(*c, *m); err != nil {
		return empty, err
	}
	baseline, err := readBoundedResults(baselinePath)
	if err != nil {
		return empty, err
	}
	candidate, err := readBoundedResults(candidatePath)
	if err != nil {
		return empty, err
	}
	if err := validateCandidateRuns(*c, *m, baseline, candidate); err != nil {
		return empty, err
	}
	report := Evaluate(c.Policy, baseline, candidate)
	if !report.Accepted {
		return report, fmt.Errorf("policy rejected: %s", report.Reason)
	}
	active, err := Load(ctx, store, project)
	if err != nil {
		return report, err
	}
	if active != nil && active.Policy.Hash() != c.SourcePolicyHash {
		return report, fmt.Errorf("baseline does not match current active policy")
	}
	if err := storePromoted(ctx, store, project, c.Policy, report); err != nil {
		return report, err
	}
	return report, nil
}
