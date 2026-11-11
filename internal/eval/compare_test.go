package eval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
)

func boolPtr(v bool) *bool        { return &v }
func floatPtr(v float64) *float64 { return &v }

func comparisonFixtures() (Record, ExternalRecord) {
	k := Record{KilnVersion: "v1", BinaryHash: "binary", Scenario: "fix-bug", Repeat: 1, DatasetHash: "fixture", Provider: "faux", Model: "faux-1", OK: true, DurationMS: 1000,
		Usage: msg.Usage{Input: 100, Output: 20}, Checks: []CheckResult{{Name: "test", Type: "command", Pass: true}, {Name: "file", Type: "file_matches", Pass: false}}}
	e := ExternalRecord{SchemaVersion: 1, Harness: "claude-code", HarnessVersion: "2.0", Model: "claude-model", ModelVersion: "2026-01", SetupHash: "setup", Scenario: "fix-bug", Repeat: 1, DatasetHash: "fixture", OK: boolPtr(true), DurationMS: 1200,
		Checks: []CheckResult{{Name: "test", Type: "command", Pass: true}, {Name: "file", Type: "file_matches", Pass: true}}, Usage: ExternalUsage{Direct: TokenUsage{Input: 80, Output: 10}, Nested: TokenUsage{Input: 30, Output: 5}, Retry: TokenUsage{Input: 10, Output: 2}}, CostUSD: floatPtr(0)}
	return k, e
}

func TestCompareRecordsAccountsForNestedAndUnknownCost(t *testing.T) {
	k, e := comparisonFixtures()
	c, err := CompareRecords([]Record{k}, []ExternalRecord{e})
	if err != nil {
		t.Fatal(err)
	}
	if c.KilnMeanScore != .5 || c.ExternalMeanScore != 1 {
		t.Fatalf("scores: %+v", c)
	}
	if c.Pairs[0].ExternalTokens != 137 || c.Pairs[0].ExternalNestedTokens != 35 || c.Pairs[0].ExternalRetryTokens != 12 {
		t.Fatalf("usage: %+v", c.Pairs[0])
	}
	if c.KilnKnownCostCount != 0 || c.KilnMeanPairedCostUSD != nil || c.ExternalKnownCostCount != 1 || c.ExternalMeanPairedCostUSD != nil || c.PairedKnownCostCount != 0 {
		t.Fatalf("cost evidence: %+v", c)
	}
	text, err := RenderComparison(c, "text")
	if err != nil || !strings.Contains(text, "unknown (0/1 known)") {
		t.Fatalf("text: %q, %v", text, err)
	}
}

func TestCompareRecordsRejectsUnmatchedOrDifferentEvidence(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Record, *ExternalRecord)
	}{
		{"dataset", func(_ *Record, e *ExternalRecord) { e.DatasetHash = "other" }},
		{"scenario", func(_ *Record, e *ExternalRecord) { e.Scenario = "other" }},
		{"check definition", func(_ *Record, e *ExternalRecord) { e.Checks[0].Type = "text_matches" }},
		{"duplicate check name", func(_ *Record, e *ExternalRecord) { e.Checks[1].Name = e.Checks[0].Name }},
		{"mixed model", func(k *Record, _ *ExternalRecord) { k.Model = "other" }},
		{"negative kiln usage", func(k *Record, _ *ExternalRecord) { k.Usage.Input = -1 }},
		{"overflow kiln usage", func(k *Record, _ *ExternalRecord) { k.Usage.Input = int(^uint(0) >> 1) }},
		{"overflow external usage", func(_ *Record, e *ExternalRecord) { e.Usage.Direct.Input = int(^uint(0) >> 1) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, e := comparisonFixtures()
			tt.change(&k, &e)
			ks, es := []Record{k}, []ExternalRecord{e}
			if tt.name == "mixed model" {
				first, _ := comparisonFixtures()
				first.Repeat = 2
				second := e
				second.Repeat = 2
				ks = append(ks, first)
				es = append(es, second)
			}
			if _, err := CompareRecords(ks, es); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestCompareRecordsRejectsMixedKilnPolicyOrRoles(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*Record)
	}{
		{"policy", func(k *Record) { k.PolicyHash = "changed" }},
		{"roles", func(k *Record) { k.Roles = map[string]string{"fast": "other/model"} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			k1, e1 := comparisonFixtures()
			k1.Provider = "kiln"
			k1.Model = "auto"
			k1.PolicyHash = "baseline"
			k1.Roles = map[string]string{"fast": "faux/faux-1"}
			k2, e2 := comparisonFixtures()
			k2.Provider = "kiln"
			k2.Model = "auto"
			k2.PolicyHash = k1.PolicyHash
			k2.Roles = map[string]string{"fast": "faux/faux-1"}
			k2.Repeat = 2
			e2.Repeat = 2
			tt.change(&k2)
			if _, err := CompareRecords([]Record{k1, k2}, []ExternalRecord{e1, e2}); err == nil {
				t.Fatal("expected mixed provenance rejection")
			}
		})
	}
}

func TestCompareRecordsCostUsesOnlyKnownPairs(t *testing.T) {
	var kiln []Record
	var external []ExternalRecord
	for repeat := 1; repeat <= 3; repeat++ {
		k, e := comparisonFixtures()
		k.Repeat = repeat
		e.Repeat = repeat
		switch repeat {
		case 1:
			k.CostUSD = .02
			e.CostUSD = nil
		case 2:
			k.CostUSD = 0
			e.CostUSD = floatPtr(.9)
		case 3:
			k.CostUSD = .03
			e.CostUSD = floatPtr(.04)
		}
		kiln = append(kiln, k)
		external = append(external, e)
	}
	c, err := CompareRecords(kiln, external)
	if err != nil {
		t.Fatal(err)
	}
	if c.KilnKnownCostCount != 2 || c.ExternalKnownCostCount != 2 || c.PairedKnownCostCount != 1 || *c.KilnMeanPairedCostUSD != .03 || *c.ExternalMeanPairedCostUSD != .04 {
		t.Fatalf("paired cost: %+v", c)
	}
}

func TestReadExternalRecordsStrictSchema(t *testing.T) {
	_, e := comparisonFixtures()
	dir := t.TempDir()
	path := filepath.Join(dir, "external.jsonl")
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		data      []byte
		wantError bool
	}{
		{"valid", data, false},
		{"internal field", append(append([]byte(nil), data[:len(data)-1]...), []byte(`,"policy_hash":"forged"}`)...), true},
		{"missing cost", nil, false},
		{"missing usage", []byte(strings.Replace(string(data), `"usage":`+string(mustUsageJSON(t, e.Usage))+`,`, "", 1)), true},
		{"null usage", []byte(strings.Replace(string(data), `"usage":`+string(mustUsageJSON(t, e.Usage)), `"usage":null`, 1)), true},
		{"missing nested", []byte(strings.Replace(string(data), `"nested":{"input":30,"output":5,"cache_read":0,"cache_write":0},`, "", 1)), true},
		{"null retry", []byte(strings.Replace(string(data), `"retry":{"input":10,"output":2,"cache_read":0,"cache_write":0}`, `"retry":null`, 1)), true},
		{"missing token field", []byte(strings.Replace(string(data), `"input":80,`, "", 1)), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := tc.data
			if tc.name == "missing cost" {
				e.CostUSD = nil
				payload, err = json.Marshal(e)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, append(payload, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			rs, readErr := ReadExternalRecords(path)
			if tc.wantError && readErr == nil {
				t.Fatal("expected schema rejection")
			}
			if !tc.wantError && (readErr != nil || len(rs) != 1) {
				t.Fatalf("records %d, error %v", len(rs), readErr)
			}
		})
	}
}

func mustUsageJSON(t *testing.T, u ExternalUsage) []byte {
	t.Helper()
	b, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
