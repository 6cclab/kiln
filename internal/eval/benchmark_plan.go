package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/learning"
	"github.com/andrepato/harness/internal/testkit/scenario"
)

// DefaultBenchmarkCohort uses checked reading and editing tasks with ordinary
// fixtures, avoiding permissions, MCP, compaction, and model-pinned scenarios.
var DefaultBenchmarkCohort = []string{"add-feature-with-tests", "answer-codebase-question", "fix-bug", "refactor-preserving-tests"}

// BenchmarkPlanOptions names local inputs. BuildBenchmarkPlan makes no model calls.
type BenchmarkPlanOptions struct {
	KilnBin        string
	ClaudeBin      string
	ScenariosDir   string
	FixturesDir    string
	ClaudeSettings string
	KilnModel      string
	ClaudeModel    string
	KilnRoles      map[string]string
	KilnPolicyHash string
	ScenarioNames  []string
	Repeats        int
	MaxTotalUSD    float64
}

// BenchmarkCheck is one exact named mechanical check, not its result.
type BenchmarkCheck struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// BenchmarkScenario pins the runner-compatible definition and fixture bytes.
type BenchmarkScenario struct {
	Name           string           `json:"name"`
	DatasetHash    string           `json:"datasetHash"`
	FixtureHash    string           `json:"fixtureHash"`
	PermissionMode string           `json:"permissionMode"`
	Checks         []BenchmarkCheck `json:"checks"`
}

// BenchmarkPlan is an offline, reviewable cohort and provenance manifest.
type BenchmarkPlan struct {
	SchemaVersion      int                 `json:"schemaVersion"`
	KilnBinaryPath     string              `json:"kilnBinaryPath"`
	KilnBinaryHash     string              `json:"kilnBinaryHash"`
	KilnVersion        string              `json:"kilnVersion"`
	KilnModel          string              `json:"kilnModel"`
	KilnRoles          map[string]string   `json:"kilnRoles,omitempty"`
	KilnPolicyHash     string              `json:"kilnPolicyHash,omitempty"`
	ClaudeBinaryPath   string              `json:"claudeBinaryPath"`
	ClaudeBinaryHash   string              `json:"claudeBinaryHash"`
	ClaudeVersion      string              `json:"claudeVersion"`
	ClaudeModel        string              `json:"claudeModel"`
	ClaudeSettingsPath string              `json:"claudeSettingsPath,omitempty"`
	ClaudeSettingsHash string              `json:"claudeSettingsHash"`
	ClaudeSetupHash    string              `json:"claudeSetupHash"`
	ScenariosDir       string              `json:"scenariosDir"`
	FixturesDir        string              `json:"fixturesDir"`
	Scenarios          []BenchmarkScenario `json:"scenarios"`
	Repeats            int                 `json:"repeats"`
	TotalPairs         int                 `json:"totalPairs"`
	TotalRuns          int                 `json:"totalRuns"`
	MaxTotalUSD        float64             `json:"maxTotalUSD"`
	ClaudePerRunCapUSD float64             `json:"claudePerRunCapUSD"`
	BudgetLimitation   string              `json:"budgetLimitation"`
	UsageLimitation    string              `json:"usageLimitation"`
}

func benchmarkVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s --version: %w", filepath.Base(path), err)
	}
	if len(output) > 4096 || strings.TrimSpace(string(output)) == "" {
		return "", fmt.Errorf("invalid --version output from %s", filepath.Base(path))
	}
	return strings.TrimSpace(string(output)), nil
}

func benchmarkFixtureRoot(fixturesDir, name string) (string, error) {
	if name == "" {
		return "", nil
	}
	if filepath.IsAbs(name) || name == "." || name == ".." || strings.HasPrefix(filepath.Clean(name), ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("fixture path escapes fixtures directory")
	}
	base, err := filepath.EvalSymlinks(fixturesDir)
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(filepath.Join(base, name))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(base, root)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("fixture path escapes fixtures directory")
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("fixture is not a directory")
	}
	return root, nil
}

func benchmarkScenario(sc *scenario.Scenario, fixturesDir string) (BenchmarkScenario, error) {
	var out BenchmarkScenario
	if sc.Name == "" || strings.TrimSpace(sc.Name) != sc.Name || strings.Contains(sc.Name, ",") || sc.Fixture == "" || sc.PermissionMode != "bypassPermissions" || len(sc.Settings) != 0 {
		return out, fmt.Errorf("scenario %q has unsupported name, fixture, permission mode or settings", sc.Name)
	}
	for _, tag := range sc.Tags {
		if strings.HasPrefix(tag, "pin-model:") {
			return out, fmt.Errorf("scenario %q pins a model", sc.Name)
		}
	}
	root, err := benchmarkFixtureRoot(fixturesDir, sc.Fixture)
	if err != nil {
		return out, err
	}
	definition := *sc
	definition.Dir = ""
	raw, err := json.Marshal(definition)
	if err != nil {
		return out, err
	}
	dataset, err := learning.Fingerprint(root, raw)
	if err != nil {
		return out, err
	}
	fixture, err := learning.Fingerprint(root, nil)
	if err != nil {
		return out, err
	}
	out = BenchmarkScenario{Name: sc.Name, DatasetHash: dataset, FixtureHash: fixture, PermissionMode: sc.PermissionMode}
	seen := map[string]bool{}
	allowed := map[string]bool{"file_matches": true, "file_equals": true, "file_absent": true, "command": true, "result_ok": true, "text_matches": true, "tool_calls": true}
	for _, check := range sc.Checks {
		name := check.Name
		if name == "" {
			name = check.Type
		}
		if !allowed[check.Type] || name == "" || seen[name] {
			return BenchmarkScenario{}, fmt.Errorf("scenario %q has unsupported or duplicate mechanical check", sc.Name)
		}
		seen[name] = true
		out.Checks = append(out.Checks, BenchmarkCheck{Name: name, Type: check.Type})
	}
	if len(out.Checks) == 0 {
		return BenchmarkScenario{}, fmt.Errorf("scenario %q has no mechanical checks", sc.Name)
	}
	return out, nil
}

// BuildBenchmarkPlan validates a reproducible local cohort without running either harness.
func BuildBenchmarkPlan(opts BenchmarkPlanOptions) (BenchmarkPlan, error) {
	var plan BenchmarkPlan
	if opts.KilnBin == "" || opts.ClaudeBin == "" || opts.ScenariosDir == "" || opts.FixturesDir == "" || opts.KilnModel == "" || opts.ClaudeModel == "" || opts.MaxTotalUSD <= 0 || math.IsNaN(opts.MaxTotalUSD) || math.IsInf(opts.MaxTotalUSD, 0) {
		return plan, fmt.Errorf("benchmark plan requires binaries, paths, exact models and finite positive budget")
	}
	if opts.Repeats == 0 {
		opts.Repeats = 5
	}
	if opts.Repeats < 2 || opts.Repeats > 20 {
		return plan, fmt.Errorf("benchmark repeats must be 2..20")
	}
	mc, err := ParseModelConfig(opts.KilnModel)
	if err != nil {
		return plan, err
	}
	if !strings.HasPrefix(opts.ClaudeModel, "claude-") || strings.Contains(opts.ClaudeModel, ",") || mc.Model != opts.ClaudeModel || mc.Faux {
		return plan, fmt.Errorf("benchmark requires the same explicit Claude model ID in both harnesses")
	}
	for _, ptr := range []*string{&opts.KilnBin, &opts.ClaudeBin, &opts.ScenariosDir, &opts.FixturesDir} {
		absolute, err := filepath.Abs(*ptr)
		if err != nil {
			return plan, err
		}
		*ptr = absolute
	}
	if opts.ClaudeSettings != "" {
		absolute, err := filepath.Abs(opts.ClaudeSettings)
		if err != nil {
			return plan, err
		}
		opts.ClaudeSettings = absolute
	}
	for _, binary := range []string{opts.KilnBin, opts.ClaudeBin} {
		if info, err := os.Stat(binary); err != nil || !info.Mode().IsRegular() {
			return plan, fmt.Errorf("benchmark binary %q is not a regular file", binary)
		}
	}
	kilnHash, err := learning.FileHash(opts.KilnBin)
	if err != nil {
		return plan, err
	}
	claudeHash, err := learning.FileHash(opts.ClaudeBin)
	if err != nil {
		return plan, err
	}
	kilnVersion, err := benchmarkVersion(opts.KilnBin)
	if err != nil {
		return plan, err
	}
	claudeVersion, err := benchmarkVersion(opts.ClaudeBin)
	if err != nil {
		return plan, err
	}
	settingsHash := learning.HashIdentity("")
	if opts.ClaudeSettings != "" {
		settingsHash, err = learning.FileHash(opts.ClaudeSettings)
		if err != nil {
			return plan, err
		}
	}
	all, err := scenario.List(opts.ScenariosDir)
	if err != nil {
		return plan, err
	}
	byName := map[string]*scenario.Scenario{}
	for _, sc := range all {
		if byName[sc.Name] != nil {
			return plan, fmt.Errorf("duplicate scenario name %q", sc.Name)
		}
		byName[sc.Name] = sc
	}
	names := opts.ScenarioNames
	if len(names) == 0 {
		names = DefaultBenchmarkCohort
	}
	if len(names) < 2 || len(names) > 20 {
		return plan, fmt.Errorf("benchmark cohort must contain 2..20 scenarios")
	}
	seen := map[string]bool{}
	plan.Scenarios = make([]BenchmarkScenario, 0, len(names))
	for _, name := range names {
		if seen[name] {
			return BenchmarkPlan{}, fmt.Errorf("duplicate selected scenario %q", name)
		}
		seen[name] = true
		sc := byName[name]
		if sc == nil {
			return BenchmarkPlan{}, fmt.Errorf("missing scenario %q", name)
		}
		planned, err := benchmarkScenario(sc, opts.FixturesDir)
		if err != nil {
			return BenchmarkPlan{}, err
		}
		plan.Scenarios = append(plan.Scenarios, planned)
	}
	sort.Slice(plan.Scenarios, func(i, j int) bool { return plan.Scenarios[i].Name < plan.Scenarios[j].Name })
	plan.SchemaVersion = 1
	plan.KilnBinaryPath = opts.KilnBin
	plan.KilnBinaryHash = kilnHash
	plan.KilnVersion = kilnVersion
	plan.KilnModel = opts.KilnModel
	plan.KilnRoles = opts.KilnRoles
	plan.KilnPolicyHash = opts.KilnPolicyHash
	plan.ClaudeBinaryPath = opts.ClaudeBin
	plan.ClaudeBinaryHash = claudeHash
	plan.ClaudeVersion = claudeVersion
	plan.ClaudeModel = opts.ClaudeModel
	plan.ClaudeSettingsPath = opts.ClaudeSettings
	plan.ClaudeSettingsHash = settingsHash
	plan.ScenariosDir = opts.ScenariosDir
	plan.FixturesDir = opts.FixturesDir
	plan.Repeats = opts.Repeats
	plan.TotalPairs = len(plan.Scenarios) * opts.Repeats
	plan.TotalRuns = 2 * plan.TotalPairs
	plan.MaxTotalUSD = opts.MaxTotalUSD
	plan.ClaudePerRunCapUSD = opts.MaxTotalUSD / (2 * float64(plan.TotalPairs))
	setupRaw, err := json.Marshal([]any{"claude-code-setup-v1", claudeHash, claudeVersion, opts.ClaudeModel, settingsHash, "--print", "--output-format=stream-json", "--verbose", "--include-partial-messages", "--forward-subagent-text", "--no-session-persistence", "--permission-mode=bypassPermissions", "--setting-sources=project,local", "--strict-mcp-config", "--max-budget-usd", plan.ClaudePerRunCapUSD})
	if err != nil {
		return BenchmarkPlan{}, err
	}
	plan.ClaudeSetupHash = learning.HashIdentity(string(setupRaw))
	plan.BudgetLimitation = "Claude --max-budget-usd can cap each print run's reported API spending; the total plan budget and kiln spending are not automatically enforced. Stop before the total budget is exceeded."
	plan.UsageLimitation = "External direct/nested/retry token counts require trace audit; unknown components cannot be entered as zero. Raw traces and actual model version must be retained and checked before comparison."
	return plan, nil
}

// ValidateBenchmarkResults checks a planned exact cohort before comparing it.
func ValidateBenchmarkResults(plan BenchmarkPlan, kiln []Record, external []ExternalRecord) (Comparison, error) {
	if plan.SchemaVersion != 1 || len(plan.Scenarios) < 2 || plan.Repeats < 2 || plan.TotalPairs != len(plan.Scenarios)*plan.Repeats || len(kiln) != plan.TotalPairs || len(external) != plan.TotalPairs {
		return Comparison{}, fmt.Errorf("results do not match benchmark plan size")
	}
	if plan.KilnBinaryHash == "" || plan.ClaudeBinaryHash == "" || plan.ClaudeSetupHash == "" || plan.KilnVersion == "" || plan.ClaudeVersion == "" {
		return Comparison{}, fmt.Errorf("benchmark plan lacks provenance")
	}
	byName := map[string]BenchmarkScenario{}
	for _, sc := range plan.Scenarios {
		if _, ok := byName[sc.Name]; ok {
			return Comparison{}, fmt.Errorf("duplicate planned scenario")
		}
		byName[sc.Name] = sc
	}
	check := func(name string, repeat int, dataset string, checks []CheckResult) error {
		sc, ok := byName[name]
		if !ok || repeat < 1 || repeat > plan.Repeats || dataset != sc.DatasetHash {
			return fmt.Errorf("unplanned scenario, repeat or dataset")
		}
		if len(checks) != len(sc.Checks) {
			return fmt.Errorf("mechanical checks differ from plan")
		}
		actual := map[string]string{}
		for _, c := range checks {
			if _, exists := actual[c.Name]; exists {
				return fmt.Errorf("duplicate check name")
			}
			actual[c.Name] = c.Type
		}
		for _, c := range sc.Checks {
			if actual[c.Name] != c.Type {
				return fmt.Errorf("mechanical checks differ from plan")
			}
		}
		return nil
	}
	batchID := ""
	for _, r := range kiln {
		if r.BinaryHash != plan.KilnBinaryHash || r.KilnVersion != plan.KilnVersion || r.Provider+"/"+r.Model != plan.KilnModel || r.PolicyHash != plan.KilnPolicyHash || !learning.SameRoles(r.Roles, plan.KilnRoles) {
			return Comparison{}, fmt.Errorf("kiln provenance differs from plan")
		}
		if batchID == "" {
			batchID = r.RunID
		}
		if r.RunID == "" || r.RunID != batchID {
			return Comparison{}, fmt.Errorf("kiln results mix run IDs")
		}
		if sc, ok := byName[r.Scenario]; !ok || r.FixtureHash != sc.FixtureHash {
			return Comparison{}, fmt.Errorf("kiln fixture hash differs from plan")
		}
		if err := check(r.Scenario, r.Repeat, r.DatasetHash, r.Checks); err != nil {
			return Comparison{}, err
		}
	}
	for _, r := range external {
		if r.Harness != "claude-code" || r.HarnessVersion != plan.ClaudeVersion || r.Model != plan.ClaudeModel || r.SetupHash != plan.ClaudeSetupHash {
			return Comparison{}, fmt.Errorf("external provenance differs from plan")
		}
		if err := check(r.Scenario, r.Repeat, r.DatasetHash, r.Checks); err != nil {
			return Comparison{}, err
		}
	}
	return CompareRecords(kiln, external)
}
