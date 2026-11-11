// Package learning validates reversible personal efficiency policies against
// paired held-out evaluation results. It never changes permissions or code.
package learning

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/andrepato/harness/internal/diag"
)

// Policy contains the only knobs an evaluated policy may change.
type Policy struct {
	SkillBudgetTokens   int  `json:"skillBudgetTokens"`
	ConservativeRouting bool `json:"conservativeRouting"`
}

// Validate rejects policies outside the supported discovery budget.
func (p Policy) Validate() error {
	if p.SkillBudgetTokens < 128 || p.SkillBudgetTokens > 8192 {
		return fmt.Errorf("skillBudgetTokens must be between 128 and 8192")
	}
	return nil
}

// Hash identifies exactly which policy produced an evaluation result.
func (p Policy) Hash() string {
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ReadPolicy accepts only the policy schema, preventing permission overrides.
func ReadPolicy(path string) (Policy, error) {
	var p Policy
	f, err := os.Open(path)
	if err != nil {
		return p, err
	}
	defer diag.Close(f)
	info, err := f.Stat()
	if err != nil {
		return p, err
	}
	if info.Size() > 8192 {
		return p, fmt.Errorf("policy exceeds 8192 bytes")
	}
	d := json.NewDecoder(io.LimitReader(f, 8192))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, err
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		return p, fmt.Errorf("policy must contain one JSON object")
	}
	return p, p.Validate()
}

// Settings returns the restricted configuration passed to an evaluated run.
func (p Policy) Settings() map[string]any {
	return map[string]any{"skillDiscovery": map[string]any{"budgetTokens": p.SkillBudgetTokens}, "modelRouter": map[string]any{"conservative": p.ConservativeRouting}}
}

// Result is the subset of kiln eval's JSONL record required by the gate.
type Result struct {
	RunID       string            `json:"run_id"`
	Scenario    string            `json:"scenario"`
	Provider    string            `json:"provider"`
	Model       string            `json:"model"`
	Version     string            `json:"kiln_version"`
	BinaryHash  string            `json:"binary_hash"`
	Repeat      int               `json:"repeat"`
	DatasetHash string            `json:"dataset_hash"`
	FixtureHash string            `json:"fixture_hash"`
	PolicyHash  string            `json:"policy_hash"`
	Tags        []string          `json:"tags"`
	Roles       map[string]string `json:"roles"`
	OK          bool              `json:"ok"`
	Score       float64           `json:"score"`
	CostUSD     float64           `json:"cost_usd"`
	DurationMS  int64             `json:"duration_ms"`
	Checks      []struct {
		Pass    bool `json:"pass"`
		Skipped bool `json:"skipped"`
	} `json:"checks"`
	Usage struct {
		Input      int `json:"input"`
		Output     int `json:"output"`
		CacheRead  int `json:"cacheRead"`
		CacheWrite int `json:"cacheWrite"`
	} `json:"usage"`
}

// Report records the evidence and reason for accepting or rejecting a policy.
type Report struct {
	BaselinePolicyHash string            `json:"baselinePolicyHash,omitempty"`
	BinaryHash         string            `json:"binaryHash"`
	Roles              map[string]string `json:"roles,omitempty"`
	Accepted           bool              `json:"accepted"`
	Reason             string            `json:"reason"`
	Pairs              int               `json:"pairs"`
	Scenarios          int               `json:"scenarios"`
	Models             []string          `json:"models"`
	TokenReduction     float64           `json:"tokenReduction"`
	CostReduction      float64           `json:"costReduction"`
	LatencyReduction   float64           `json:"latencyReduction"`
}

func readResults(path string) ([]Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer diag.Close(f)
	return readResultsReader(f)
}

func readResultsReader(source io.Reader) ([]Result, error) {
	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	var out []Result
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var r Result
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
		if len(out) > 100000 {
			return nil, fmt.Errorf("too many evaluation records")
		}
	}
	return out, scanner.Err()
}

// EvaluateFiles compares already-produced results; it makes no model calls.
func EvaluateFiles(policy Policy, baseline, candidate string) (Report, error) {
	a, err := readResults(baseline)
	if err != nil {
		return Report{}, err
	}
	b, err := readResults(candidate)
	if err != nil {
		return Report{}, err
	}
	return Evaluate(policy, a, b), nil
}

func key(r Result) string {
	return fmt.Sprintf("%s\x00%s/%s\x00%d", r.Scenario, r.Provider, r.Model, r.Repeat)
}
func heldOut(r Result) bool {
	for _, tag := range r.Tags {
		if tag == "holdout" {
			return true
		}
	}
	return false
}
func checked(r Result) bool {
	n := 0
	for _, c := range r.Checks {
		if !c.Skipped {
			n++
			if !c.Pass {
				return false
			}
		}
	}
	return n > 0
}
func tokens(r Result) float64 {
	return float64(r.Usage.Input + r.Usage.Output + r.Usage.CacheRead + r.Usage.CacheWrite)
}
func reduction(before, after float64) float64 {
	if before <= 0 {
		return 0
	}
	return (before - after) / before
}

// Evaluate requires at least 20 matched runs over five held-out tasks. Every
// candidate must pass mechanical checks, preserve score, and use the same
// binary, fixture fingerprint, model and role mapping as its baseline. Faux
// runs validate harness behavior but cannot authorize learned policy promotion.
func Evaluate(policy Policy, baseline, candidate []Result) Report {
	report := Report{}
	reject := func(reason string) Report { report.Reason = reason; return report }
	if err := policy.Validate(); err != nil {
		return reject(err.Error())
	}
	if len(baseline) != len(candidate) || len(baseline) < 20 {
		return reject("need at least 20 matched baseline and candidate runs")
	}
	indexed := map[string]Result{}
	for _, r := range baseline {
		if _, ok := indexed[key(r)]; ok {
			return reject("duplicate baseline run")
		}
		indexed[key(r)] = r
	}
	seen := map[string]bool{}
	scenarios := map[string]bool{}
	models := map[string]bool{}
	var beforeTokens, afterTokens, beforeCost, afterCost, beforeTime, afterTime float64
	knownCosts := true
	for _, r := range candidate {
		prior, ok := indexed[key(r)]
		if !ok || seen[key(r)] {
			return reject("unmatched or duplicate candidate run")
		}
		seen[key(r)] = true
		if r.Provider == "faux" || !heldOut(r) || !heldOut(prior) {
			return reject("promotion requires real-model held-out runs")
		}
		if r.RunID == "" || prior.RunID == "" || r.RunID == prior.RunID {
			return reject("baseline and candidate must be separate recorded runs")
		}
		if prior.PolicyHash == r.PolicyHash {
			return reject("baseline and candidate use the same policy")
		}
		if r.PolicyHash != policy.Hash() || r.DatasetHash == "" || r.DatasetHash != prior.DatasetHash || r.Version == "" || r.Version != prior.Version || r.BinaryHash == "" || r.BinaryHash != prior.BinaryHash {
			return reject("policy, fixture or binary provenance mismatch")
		}
		roleA, _ := json.Marshal(prior.Roles)
		roleB, _ := json.Marshal(r.Roles)
		if string(roleA) != string(roleB) {
			return reject("model roles changed between paired runs")
		}
		if math.IsNaN(r.Score) || math.IsNaN(prior.Score) || r.Score < 0 || r.Score > 1 || prior.Score < 0 || prior.Score > 1 || r.CostUSD < 0 || prior.CostUSD < 0 || math.IsInf(r.CostUSD, 0) || math.IsInf(prior.CostUSD, 0) || math.IsNaN(r.CostUSD) || math.IsNaN(prior.CostUSD) {
			return reject("invalid score or cost evidence")
		}
		if r.Provider == "kiln" && r.Model == "auto" && len(r.Roles) == 0 {
			return reject("automatic routing evaluation requires explicit model roles")
		}
		if !r.OK || !prior.OK || !checked(r) || !checked(prior) || r.Score < prior.Score {
			return reject("failed checks or quality regression")
		}
		if tokens(r) <= 0 || tokens(prior) <= 0 || r.DurationMS <= 0 || prior.DurationMS <= 0 {
			return reject("missing usage or timing evidence")
		}
		if report.Pairs == 0 {
			report.BaselinePolicyHash = prior.PolicyHash
			report.BinaryHash = r.BinaryHash
			report.Roles = r.Roles
		} else if report.BinaryHash != r.BinaryHash || report.BaselinePolicyHash != prior.PolicyHash || !SameRoles(report.Roles, r.Roles) {
			return reject("candidate batch mixes binaries or model-role mappings")
		}
		report.Pairs++
		scenarios[r.Scenario] = true
		models[r.Provider+"/"+r.Model] = true
		beforeTokens += tokens(prior)
		afterTokens += tokens(r)
		beforeCost += prior.CostUSD
		afterCost += r.CostUSD
		beforeTime += float64(prior.DurationMS)
		afterTime += float64(r.DurationMS)
		if prior.CostUSD <= 0 || r.CostUSD <= 0 {
			knownCosts = false
		}
	}
	report.Scenarios = len(scenarios)
	if report.Scenarios < 5 {
		return reject("need at least five distinct held-out tasks")
	}
	for model := range models {
		report.Models = append(report.Models, model)
	}
	sort.Strings(report.Models)
	report.TokenReduction = reduction(beforeTokens, afterTokens)
	if knownCosts {
		report.CostReduction = reduction(beforeCost, afterCost)
	}
	report.LatencyReduction = reduction(beforeTime, afterTime)
	if report.CostReduction < 0.05 && report.LatencyReduction < 0.2 {
		return reject("no material measured efficiency improvement")
	}
	// Do not purchase a token/latency improvement with higher measured cost.
	if knownCosts && report.CostReduction < 0 {
		return reject("candidate increases measured cost")
	}
	report.Accepted = true
	report.Reason = "matched held-out checks passed without score regression; efficiency improved"
	return report
}

// Fingerprint binds a scenario definition to the bytes of its fixture files.
func Fingerprint(root string, definition []byte) (string, error) {
	h := sha256.New()
	enc := json.NewEncoder(h)
	// A versioned, framed manifest prevents file boundaries, symlink targets
	// and scenario text from being mistaken for one another's bytes.
	definitionHash := sha256.Sum256(definition)
	if err := enc.Encode([]string{"kiln-fixture-v2", hex.EncodeToString(definitionHash[:])}); err != nil {
		return "", err
	}
	if root != "" {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			kind, content := "file", ""
			switch {
			case entry.IsDir():
				kind = "directory"
			case entry.Type()&os.ModeSymlink != 0:
				kind = "symlink"
				content, err = os.Readlink(path)
			case info.Mode().IsRegular():
				content, err = FileHash(path)
			default:
				return fmt.Errorf("unsupported fixture file: %s", rel)
			}
			if err != nil {
				return err
			}
			return enc.Encode([]string{kind, filepath.ToSlash(rel), fmt.Sprintf("%o", info.Mode().Perm()), content})
		})
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// FileHash hashes the exact evaluated binary, independent of its version label.
func FileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer diag.Close(f)
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
