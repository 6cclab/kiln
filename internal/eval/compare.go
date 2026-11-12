package eval

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/andrepato/harness/internal/diag"
)

// TokenUsage is one nonoverlapping part of an external harness run's usage.
type TokenUsage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
}

func (u TokenUsage) total() int { return u.Input + u.Output + u.CacheRead + u.CacheWrite }
func (u TokenUsage) valid() bool {
	return u.Input >= 0 && u.Output >= 0 && u.CacheRead >= 0 && u.CacheWrite >= 0
}

// UnmarshalJSON requires all usage counters, including counters known to be zero.
func (u *TokenUsage) UnmarshalJSON(data []byte) error {
	var fields struct {
		Input      *int `json:"input"`
		Output     *int `json:"output"`
		CacheRead  *int `json:"cache_read"`
		CacheWrite *int `json:"cache_write"`
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("token usage cannot be null")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&fields); err != nil {
		return err
	}
	if fields.Input == nil || fields.Output == nil || fields.CacheRead == nil || fields.CacheWrite == nil {
		return fmt.Errorf("token usage requires input, output, cache_read and cache_write")
	}
	*u = TokenUsage{Input: *fields.Input, Output: *fields.Output, CacheRead: *fields.CacheRead, CacheWrite: *fields.CacheWrite}
	return nil
}

func checkedTokens(usages ...TokenUsage) (int, error) {
	maxInt := int(^uint(0) >> 1)
	total := 0
	for _, u := range usages {
		if !u.valid() {
			return 0, fmt.Errorf("usage counts must be nonnegative")
		}
		for _, count := range [...]int{u.Input, u.Output, u.CacheRead, u.CacheWrite} {
			if count > maxInt-total {
				return 0, fmt.Errorf("usage token count overflows int")
			}
			total += count
		}
	}
	return total, nil
}

// ExternalUsage separates top-level requests, nested agents and retries.
// The three fields must be disjoint; their sum is the full run usage.
type ExternalUsage struct {
	Direct TokenUsage `json:"direct"`
	Nested TokenUsage `json:"nested"`
	Retry  TokenUsage `json:"retry"`
}

// UnmarshalJSON requires each disjoint usage component explicitly.
func (u *ExternalUsage) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("usage cannot be null")
	}
	var fields struct {
		Direct *TokenUsage `json:"direct"`
		Nested *TokenUsage `json:"nested"`
		Retry  *TokenUsage `json:"retry"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&fields); err != nil {
		return err
	}
	if fields.Direct == nil || fields.Nested == nil || fields.Retry == nil {
		return fmt.Errorf("usage requires direct, nested and retry")
	}
	*u = ExternalUsage{Direct: *fields.Direct, Nested: *fields.Nested, Retry: *fields.Retry}
	return nil
}

// ExternalRecord is a deliberately separate wire format from kiln Record.
// It cannot supply the internal policy, binary or run provenance required by
// the personal-policy promotion gate.
type ExternalRecord struct {
	SchemaVersion  int           `json:"schema_version"`
	Harness        string        `json:"harness"`
	HarnessVersion string        `json:"harness_version"`
	Model          string        `json:"model"`
	ModelVersion   string        `json:"model_version"`
	SetupHash      string        `json:"setup_hash"`
	Scenario       string        `json:"scenario"`
	Repeat         int           `json:"repeat"`
	DatasetHash    string        `json:"dataset_hash"`
	OK             *bool         `json:"ok"`
	DurationMS     int64         `json:"duration_ms"`
	Checks         []CheckResult `json:"checks"`
	Usage          ExternalUsage `json:"usage"`
	CostUSD        *float64      `json:"cost_usd"`
}

// ComparisonPair contains a matched run. Nil costs mean unknown, including
// legacy kiln records whose zero cost did not encode whether pricing existed.
type ComparisonPair struct {
	Scenario             string   `json:"scenario"`
	Repeat               int      `json:"repeat"`
	DatasetHash          string   `json:"dataset_hash"`
	KilnScore            float64  `json:"kiln_score"`
	ExternalScore        float64  `json:"external_score"`
	KilnDurationMS       int64    `json:"kiln_duration_ms"`
	ExternalDurationMS   int64    `json:"external_duration_ms"`
	KilnTokens           int      `json:"kiln_tokens"`
	ExternalTokens       int      `json:"external_tokens"`
	ExternalDirectTokens int      `json:"external_direct_tokens"`
	ExternalNestedTokens int      `json:"external_nested_tokens"`
	ExternalRetryTokens  int      `json:"external_retry_tokens"`
	KilnCostUSD          *float64 `json:"kiln_cost_usd"`
	ExternalCostUSD      *float64 `json:"external_cost_usd"`
}

// Comparison is an exact-cohort, mechanical-check comparison of two harnesses.
type Comparison struct {
	KilnVersion               string            `json:"kiln_version"`
	KilnBinaryHash            string            `json:"kiln_binary_hash"`
	KilnModel                 string            `json:"kiln_model"`
	KilnPolicyHash            string            `json:"kiln_policy_hash,omitempty"`
	KilnRoles                 map[string]string `json:"kiln_roles,omitempty"`
	ExternalHarness           string            `json:"external_harness"`
	ExternalHarnessVersion    string            `json:"external_harness_version"`
	ExternalModel             string            `json:"external_model"`
	ExternalModelVersion      string            `json:"external_model_version"`
	ExternalSetupHash         string            `json:"external_setup_hash"`
	Pairs                     []ComparisonPair  `json:"pairs"`
	KilnMeanScore             float64           `json:"kiln_mean_score"`
	ExternalMeanScore         float64           `json:"external_mean_score"`
	KilnMeanDurationMS        float64           `json:"kiln_mean_duration_ms"`
	ExternalMeanDurationMS    float64           `json:"external_mean_duration_ms"`
	KilnMeanTokens            float64           `json:"kiln_mean_tokens"`
	ExternalMeanTokens        float64           `json:"external_mean_tokens"`
	KilnKnownCostCount        int               `json:"kiln_known_cost_count"`
	ExternalKnownCostCount    int               `json:"external_known_cost_count"`
	PairedKnownCostCount      int               `json:"paired_known_cost_count"`
	KilnMeanPairedCostUSD     *float64          `json:"kiln_mean_paired_cost_usd"`
	ExternalMeanPairedCostUSD *float64          `json:"external_mean_paired_cost_usd"`
}

func validateChecks(checks []CheckResult) (map[string]bool, error) {
	if len(checks) == 0 {
		return nil, fmt.Errorf("no mechanical checks")
	}
	seen := make(map[string]bool, len(checks))
	names := make(map[string]bool, len(checks))
	for _, c := range checks {
		if c.Name == "" || c.Type == "" || c.Skipped {
			return nil, fmt.Errorf("checks need name/type and cannot be skipped")
		}
		key := c.Name + "\x00" + c.Type
		if names[c.Name] {
			return nil, fmt.Errorf("duplicate check name %s", c.Name)
		}
		seen[key] = c.Pass
		names[c.Name] = true
	}
	return seen, nil
}

func mechanicalScore(checks map[string]bool, ok bool) float64 {
	if !ok {
		return 0
	}
	passed := 0
	for _, pass := range checks {
		if pass {
			passed++
		}
	}
	return float64(passed) / float64(len(checks))
}

// ReadExternalRecords reads versioned external JSONL with unknown fields
// rejected, so internal eval records cannot be silently accepted here.
func ReadExternalRecords(path string) ([]ExternalRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("external records: %w", err)
	}
	defer diag.Close(f)
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var out []ExternalRecord
	for line := 1; s.Scan(); line++ {
		if len(bytes.TrimSpace(s.Bytes())) == 0 {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(s.Bytes(), &raw); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, line, err)
		}
		usageRaw, present := raw["usage"]
		if !present || bytes.Equal(bytes.TrimSpace(usageRaw), []byte("null")) {
			return nil, fmt.Errorf("%s line %d: usage is required", path, line)
		}
		dec := json.NewDecoder(bytes.NewReader(s.Bytes()))
		dec.DisallowUnknownFields()
		var r ExternalRecord
		if err := dec.Decode(&r); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, line, err)
		}
		var extra any
		if err := dec.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("%s line %d: trailing JSON", path, line)
		}
		if err := validateExternal(r); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, line, err)
		}
		out = append(out, r)
	}
	if err := s.Err(); err != nil {
		return nil, fmt.Errorf("external records: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("external records: empty file")
	}
	return out, nil
}

func validateExternal(r ExternalRecord) error {
	if r.SchemaVersion != 1 {
		return fmt.Errorf("schema_version must be 1")
	}
	if r.Harness == "" || r.Harness == "kiln" || r.HarnessVersion == "" || r.Model == "" || r.ModelVersion == "" || r.SetupHash == "" {
		return fmt.Errorf("external harness/model/setup provenance is required")
	}
	if r.Scenario == "" || r.Repeat < 1 || r.DatasetHash == "" || r.OK == nil || r.DurationMS <= 0 {
		return fmt.Errorf("scenario, repeat, dataset_hash, ok and positive duration_ms are required")
	}
	if _, err := validateChecks(r.Checks); err != nil {
		return err
	}
	tokens, err := checkedTokens(r.Usage.Direct, r.Usage.Nested, r.Usage.Retry)
	if err != nil {
		return err
	}
	if tokens <= 0 {
		return fmt.Errorf("usage must include at least one token")
	}
	if r.CostUSD != nil && (*r.CostUSD < 0 || math.IsNaN(*r.CostUSD) || math.IsInf(*r.CostUSD, 0)) {
		return fmt.Errorf("cost_usd must be finite and nonnegative")
	}
	return nil
}

func comparisonKey(s string, repeat int) string { return fmt.Sprintf("%s\x00%d", s, repeat) }

// CompareRecords requires exactly matched cohorts and identical check names
// and types. Dataset hashes bind each pair to the same fixture and scenario.
func CompareRecords(kiln []Record, external []ExternalRecord) (Comparison, error) {
	if len(kiln) == 0 || len(kiln) != len(external) {
		return Comparison{}, fmt.Errorf("cohorts differ: %d kiln and %d external records", len(kiln), len(external))
	}
	var out Comparison
	byKey := make(map[string]Record, len(kiln))
	for _, r := range kiln {
		if r.Scenario == "" || r.Repeat < 1 || r.DatasetHash == "" || r.DurationMS <= 0 || r.KilnVersion == "" || r.BinaryHash == "" {
			return out, fmt.Errorf("kiln record lacks required provenance or timing")
		}
		kilnUsage := TokenUsage{Input: r.Usage.Input, Output: r.Usage.Output, CacheRead: r.Usage.CacheRead, CacheWrite: r.Usage.CacheWrite}
		kilnTokens, err := checkedTokens(kilnUsage)
		if err != nil {
			return out, fmt.Errorf("kiln record %s/%d: %w", r.Scenario, r.Repeat, err)
		}
		if kilnTokens <= 0 {
			return out, fmt.Errorf("kiln record %s/%d lacks usage", r.Scenario, r.Repeat)
		}
		if _, err := validateChecks(r.Checks); err != nil {
			return out, fmt.Errorf("kiln record %s/%d: %w", r.Scenario, r.Repeat, err)
		}
		if r.CostUSD < 0 || math.IsNaN(r.CostUSD) || math.IsInf(r.CostUSD, 0) {
			return out, fmt.Errorf("kiln record has invalid cost")
		}
		model := r.Provider + "/" + r.Model
		if out.KilnVersion == "" {
			out.KilnVersion, out.KilnBinaryHash, out.KilnModel, out.KilnPolicyHash, out.KilnRoles = r.KilnVersion, r.BinaryHash, model, r.PolicyHash, r.Roles
		}
		if out.KilnVersion != r.KilnVersion || out.KilnBinaryHash != r.BinaryHash || out.KilnModel != model || out.KilnPolicyHash != r.PolicyHash || !reflect.DeepEqual(out.KilnRoles, r.Roles) {
			return out, fmt.Errorf("kiln cohort mixes binary, model, policy or role provenance")
		}
		key := comparisonKey(r.Scenario, r.Repeat)
		if _, exists := byKey[key]; exists {
			return out, fmt.Errorf("duplicate kiln record %s/%d", r.Scenario, r.Repeat)
		}
		byKey[key] = r
	}
	seen := make(map[string]bool, len(external))
	var sumKilnCost, sumExternalCost float64
	for _, r := range external {
		if err := validateExternal(r); err != nil {
			return out, fmt.Errorf("external record %s/%d: %w", r.Scenario, r.Repeat, err)
		}
		if out.ExternalHarness == "" {
			out.ExternalHarness, out.ExternalHarnessVersion, out.ExternalModel, out.ExternalModelVersion, out.ExternalSetupHash = r.Harness, r.HarnessVersion, r.Model, r.ModelVersion, r.SetupHash
		}
		if out.ExternalHarness != r.Harness || out.ExternalHarnessVersion != r.HarnessVersion || out.ExternalModel != r.Model || out.ExternalModelVersion != r.ModelVersion || out.ExternalSetupHash != r.SetupHash {
			return out, fmt.Errorf("external cohort mixes harness, model or setup provenance")
		}
		key := comparisonKey(r.Scenario, r.Repeat)
		k, exists := byKey[key]
		if !exists {
			return out, fmt.Errorf("unmatched external record %s/%d", r.Scenario, r.Repeat)
		}
		if seen[key] {
			return out, fmt.Errorf("duplicate external record %s/%d", r.Scenario, r.Repeat)
		}
		seen[key] = true
		if k.DatasetHash != r.DatasetHash {
			return out, fmt.Errorf("dataset hash mismatch for %s/%d", r.Scenario, r.Repeat)
		}
		kc, _ := validateChecks(k.Checks)
		ec, _ := validateChecks(r.Checks)
		if len(kc) != len(ec) {
			return out, fmt.Errorf("check set mismatch for %s/%d", r.Scenario, r.Repeat)
		}
		for name := range kc {
			if _, exists := ec[name]; !exists {
				return out, fmt.Errorf("check set mismatch for %s/%d", r.Scenario, r.Repeat)
			}
		}
		p := ComparisonPair{Scenario: r.Scenario, Repeat: r.Repeat, DatasetHash: r.DatasetHash, KilnScore: mechanicalScore(kc, k.OK), ExternalScore: mechanicalScore(ec, *r.OK), KilnDurationMS: k.DurationMS, ExternalDurationMS: r.DurationMS,
			KilnTokens:           k.Usage.Input + k.Usage.Output + k.Usage.CacheRead + k.Usage.CacheWrite,
			ExternalDirectTokens: r.Usage.Direct.total(), ExternalNestedTokens: r.Usage.Nested.total(), ExternalRetryTokens: r.Usage.Retry.total(), ExternalTokens: r.Usage.Direct.total() + r.Usage.Nested.total() + r.Usage.Retry.total(), ExternalCostUSD: r.CostUSD}
		if k.CostUSD > 0 {
			cost := k.CostUSD
			p.KilnCostUSD = &cost
		}
		if p.KilnCostUSD != nil {
			out.KilnKnownCostCount++
		}
		if p.ExternalCostUSD != nil {
			out.ExternalKnownCostCount++
		}
		if p.KilnCostUSD != nil && p.ExternalCostUSD != nil {
			sumKilnCost += *p.KilnCostUSD
			sumExternalCost += *p.ExternalCostUSD
			out.PairedKnownCostCount++
		}
		out.KilnMeanScore += p.KilnScore
		out.ExternalMeanScore += p.ExternalScore
		out.KilnMeanDurationMS += float64(p.KilnDurationMS)
		out.ExternalMeanDurationMS += float64(p.ExternalDurationMS)
		out.KilnMeanTokens += float64(p.KilnTokens)
		out.ExternalMeanTokens += float64(p.ExternalTokens)
		out.Pairs = append(out.Pairs, p)
	}
	for key, r := range byKey {
		if !seen[key] {
			return out, fmt.Errorf("unmatched kiln record %s/%d", r.Scenario, r.Repeat)
		}
	}
	sort.Slice(out.Pairs, func(i, j int) bool {
		if out.Pairs[i].Scenario != out.Pairs[j].Scenario {
			return out.Pairs[i].Scenario < out.Pairs[j].Scenario
		}
		return out.Pairs[i].Repeat < out.Pairs[j].Repeat
	})
	n := float64(len(out.Pairs))
	out.KilnMeanScore /= n
	out.ExternalMeanScore /= n
	out.KilnMeanDurationMS /= n
	out.ExternalMeanDurationMS /= n
	out.KilnMeanTokens /= n
	out.ExternalMeanTokens /= n
	if out.PairedKnownCostCount > 0 {
		kilnMean := sumKilnCost / float64(out.PairedKnownCostCount)
		externalMean := sumExternalCost / float64(out.PairedKnownCostCount)
		out.KilnMeanPairedCostUSD = &kilnMean
		out.ExternalMeanPairedCostUSD = &externalMean
	}
	return out, nil
}

// CompareFiles reads kiln and external JSONL and returns a matched report.
func CompareFiles(kilnPath, externalPath string) (Comparison, error) {
	k, err := ReadRecords(kilnPath)
	if err != nil {
		return Comparison{}, err
	}
	e, err := ReadExternalRecords(externalPath)
	if err != nil {
		return Comparison{}, err
	}
	return CompareRecords(k, e)
}

// RenderComparison returns a compact text or JSON report.
func RenderComparison(c Comparison, format string) (string, error) {
	if format == "json" {
		b, err := json.MarshalIndent(c, "", "  ")
		if err != nil {
			return "", err
		}
		return string(b) + "\n", nil
	}
	if format != "" && format != "text" {
		return "", fmt.Errorf("unknown comparison format %q", format)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Matched runs: %d\nKiln: %s (%s; %s)\nExternal: %s %s, model %s %s, setup %s\n", len(c.Pairs), c.KilnModel, c.KilnVersion, c.KilnBinaryHash, c.ExternalHarness, c.ExternalHarnessVersion, c.ExternalModel, c.ExternalModelVersion, c.ExternalSetupHash)
	fmt.Fprintf(&b, "Quality (mechanical pass rate): kiln %.3f, external %.3f\n", c.KilnMeanScore, c.ExternalMeanScore)
	fmt.Fprintf(&b, "Mean duration (ms): kiln %.0f, external %.0f\nMean tokens: kiln %.0f, external %.0f (direct + nested + retry)\n", c.KilnMeanDurationMS, c.ExternalMeanDurationMS, c.KilnMeanTokens, c.ExternalMeanTokens)
	formatCost := func(v *float64, known int) string {
		if v == nil {
			return fmt.Sprintf("unknown (0/%d known)", len(c.Pairs))
		}
		return fmt.Sprintf("$%.6f (%d/%d known)", *v, known, len(c.Pairs))
	}
	fmt.Fprintf(&b, "Mean paired known cost (%d/%d pairs; kiln %d known, external %d known): kiln %s, external %s\n", c.PairedKnownCostCount, len(c.Pairs), c.KilnKnownCostCount, c.ExternalKnownCostCount, formatCost(c.KilnMeanPairedCostUSD, c.PairedKnownCostCount), formatCost(c.ExternalMeanPairedCostUSD, c.PairedKnownCostCount))
	return b.String(), nil
}
