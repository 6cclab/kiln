package learning

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/memory"
	"github.com/andrepato/harness/internal/testkit/scenario"
)

// PlannedScenario binds one held-out scenario to the runner's fixture fingerprint.
type PlannedScenario struct {
	Name        string `json:"name"`
	DatasetHash string `json:"datasetHash"`
	FixtureHash string `json:"fixtureHash"`
}

// Manifest names exact inputs for a paired evaluation. It does not execute it.
type Manifest struct {
	CandidateID        string            `json:"candidateID"`
	ScenarioDir        string            `json:"scenarioDir"`
	FixturesDir        string            `json:"fixturesDir"`
	Scenarios          []PlannedScenario `json:"scenarios"`
	ExactNames         []string          `json:"exactNames"`
	BaselinePolicy     Policy            `json:"baselinePolicy"`
	BaselinePolicyHash string            `json:"baselinePolicyHash"`
	CandidatePolicy    Policy            `json:"candidatePolicy"`
	Model              string            `json:"model"`
	Roles              map[string]string `json:"roles"`
	BinaryHash         string            `json:"binaryHash"`
	Version            string            `json:"version"`
	Repeats            int               `json:"repeats"`
	Pairs              int               `json:"pairs"`
	TotalRuns          int               `json:"totalRuns"`
	EstimatedUSD       float64           `json:"estimatedUSD"`
	EstimateCaveat     string            `json:"estimateCaveat"`
	CreatedAt          int64             `json:"createdAt"`
}

// PlanScope is separate from candidate and active policy records.
func PlanScope(project string) string { return "policy-plan:" + project }

// LoadCandidate returns a queued candidate by its exact ID and project.
func LoadCandidate(ctx context.Context, store *memory.Store, project, id string) (*Candidate, error) {
	if len(id) != 64 {
		return nil, fmt.Errorf("invalid candidate ID")
	}
	r, err := store.Current(ctx, CandidateScope(project), id, "")
	if err != nil || r == nil {
		return nil, err
	}
	var c Candidate
	if err := json.Unmarshal([]byte(r.Text), &c); err != nil {
		return nil, err
	}
	if c.ID != id || c.SourcePolicy.Validate() != nil || c.SourcePolicy.Hash() != c.SourcePolicyHash || c.SourceBinaryHash == "" || c.SourceModel == "" || c.SourceResultsHash == "" || len(c.SourceDatasets) < 5 || len(c.SourceFixtures) < 5 || c.Policy.Validate() != nil {
		return nil, fmt.Errorf("queued candidate lacks required provenance")
	}
	return &c, nil
}

func fixtureRoot(fixturesDir, fixture string) (string, error) {
	if fixture == "" {
		return "", nil
	}
	if filepath.IsAbs(fixture) || fixture == "." || fixture == ".." || strings.HasPrefix(filepath.Clean(fixture), ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("fixture path escapes fixtures directory")
	}
	base, err := filepath.EvalSymlinks(fixturesDir)
	if err != nil {
		return "", err
	}
	full, err := filepath.EvalSymlinks(filepath.Join(base, fixture))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(base, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("fixture path escapes fixtures directory")
	}
	info, err := os.Stat(full)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("fixture is not a directory")
	}
	return full, nil
}

func scenarioFingerprint(sc *scenario.Scenario, fixturesDir string) (string, string, error) {
	root, err := fixtureRoot(fixturesDir, sc.Fixture)
	if err != nil {
		return "", "", err
	}
	definition := *sc
	definition.Dir = ""
	raw, err := json.Marshal(definition)
	if err != nil {
		return "", "", err
	}
	dataset, err := Fingerprint(root, raw)
	if err != nil {
		return "", "", err
	}
	fixture, err := Fingerprint(root, nil)
	return dataset, fixture, err
}

func validatePlannedScenarioMetadata(sc *scenario.Scenario, model string) error {
	if sc.Name == "" || strings.TrimSpace(sc.Name) != sc.Name || strings.Contains(sc.Name, ",") {
		return fmt.Errorf("held-out scenario name %q cannot be used by the exact-name filter", sc.Name)
	}
	holdout, pins := false, 0
	for _, tag := range sc.Tags {
		if tag == "holdout" {
			holdout = true
		}
		if strings.HasPrefix(tag, "pin-model:") {
			pins++
			if strings.TrimPrefix(tag, "pin-model:") != model {
				return fmt.Errorf("held-out scenario %q pins a different model", sc.Name)
			}
		}
	}
	if pins > 1 {
		return fmt.Errorf("held-out scenario %q has multiple model pins", sc.Name)
	}
	if !holdout || len(sc.Checks) == 0 {
		return fmt.Errorf("scenario %q is not a checked holdout", sc.Name)
	}
	if _, ok := sc.Settings["modelRoles"]; ok {
		return fmt.Errorf("held-out scenario %q overrides model roles", sc.Name)
	}
	return nil
}

func collectHeldOut(c Candidate, scenarioDir, fixturesDir string) ([]PlannedScenario, error) {
	all, err := scenario.List(scenarioDir)
	if err != nil {
		return nil, err
	}
	sourceNames := map[string]bool{}
	sourceHashes := map[string]bool{}
	sourceFixtures := map[string]bool{}
	for _, name := range c.SourceScenarioNames {
		sourceNames[name] = true
	}
	for _, hash := range c.SourceDatasets {
		sourceHashes[hash] = true
	}
	for _, hash := range c.SourceFixtures {
		sourceFixtures[hash] = true
	}
	seen := map[string]bool{}
	out := make([]PlannedScenario, 0, 5)
	for _, sc := range all {
		if seen[sc.Name] {
			return nil, fmt.Errorf("duplicate scenario name %q", sc.Name)
		}
		seen[sc.Name] = true
		if len(out) == 5 {
			continue
		}
		if sourceNames[sc.Name] {
			continue
		}
		holdout := false
		for _, tag := range sc.Tags {
			if tag == "holdout" {
				holdout = true
				break
			}
		}
		if !holdout {
			continue
		}
		if err := validatePlannedScenarioMetadata(sc, c.SourceModel); err != nil {
			return nil, err
		}
		hash, fixture, err := scenarioFingerprint(sc, fixturesDir)
		if err != nil {
			return nil, fmt.Errorf("held-out scenario %q: %w", sc.Name, err)
		}
		if sourceHashes[hash] || sourceFixtures[fixture] {
			continue
		}
		out = append(out, PlannedScenario{Name: sc.Name, DatasetHash: hash, FixtureHash: fixture})
	}
	if len(out) < 5 {
		return nil, fmt.Errorf("need five distinct held-out scenarios absent from source by name and fixture fingerprint")
	}
	return out, nil
}

// PlanCandidate validates local held-out scenarios and stores a concrete manifest.
func PlanCandidate(ctx context.Context, store *memory.Store, project, id, scenarioDir, fixturesDir string) (Manifest, error) {
	var manifest Manifest
	c, err := LoadCandidate(ctx, store, project, id)
	if err != nil {
		return manifest, err
	}
	if c == nil {
		return manifest, fmt.Errorf("candidate not found")
	}
	scenarioDir, err = filepath.Abs(scenarioDir)
	if err != nil {
		return manifest, err
	}
	scenarioDir, err = filepath.EvalSymlinks(scenarioDir)
	if err != nil {
		return manifest, err
	}
	fixturesDir, err = filepath.Abs(fixturesDir)
	if err != nil {
		return manifest, err
	}
	fixturesDir, err = filepath.EvalSymlinks(fixturesDir)
	if err != nil {
		return manifest, err
	}
	selected, err := collectHeldOut(*c, scenarioDir, fixturesDir)
	if err != nil {
		return manifest, err
	}
	exactNames := make([]string, 0, len(selected))
	for _, sc := range selected {
		exactNames = append(exactNames, sc.Name)
	}
	manifest = Manifest{CandidateID: id, ScenarioDir: scenarioDir, FixturesDir: fixturesDir, Scenarios: selected,
		ExactNames:     exactNames,
		BaselinePolicy: c.SourcePolicy, BaselinePolicyHash: c.SourcePolicyHash, CandidatePolicy: c.Policy, Model: c.SourceModel, Roles: c.SourceRoles,
		BinaryHash: c.SourceBinaryHash, Version: c.SourceVersion, Repeats: 4, Pairs: 20, TotalRuns: 40,
		EstimatedUSD: c.Plan.EstimatedUSD, EstimateCaveat: "Estimate from source mean cost; not an execution or spend cap.", CreatedAt: time.Now().Unix()}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return Manifest{}, err
	}
	if _, err := store.Put(ctx, memory.Record{Scope: PlanScope(project), Key: id, Kind: "procedure", Text: string(raw), Evidence: "Validated local held-out scenario definitions and fixture fingerprints; no evaluation run"}); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// LoadManifest reads the exact stored plan for a candidate.
func LoadManifest(ctx context.Context, store *memory.Store, project, id string) (*Manifest, error) {
	r, err := store.Current(ctx, PlanScope(project), id, "")
	if err != nil || r == nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal([]byte(r.Text), &m); err != nil {
		return nil, err
	}
	if m.CandidateID != id || len(m.Scenarios) != 5 || m.Repeats != 4 || m.Pairs != 20 || m.TotalRuns != 40 {
		return nil, fmt.Errorf("invalid stored candidate plan")
	}
	return &m, nil
}
