package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/andrepato/harness/internal/eval"
	"github.com/andrepato/harness/internal/msg"
)

func TestEvalCompareCommandUsesOfflinePairedEvidence(t *testing.T) {
	dir := t.TempDir()
	checks := []eval.CheckResult{{Name: "test", Type: "command", Pass: true}}
	kiln := eval.Record{Scenario: "fix", Repeat: 1, DatasetHash: "fixture", KilnVersion: "test", BinaryHash: "binary", Provider: "faux", Model: "faux-1", OK: true, DurationMS: 100, Checks: checks, Usage: msg.Usage{Input: 100}, CostUSD: .01}
	ok := true
	external := eval.ExternalRecord{SchemaVersion: 1, Harness: "other", HarnessVersion: "test", Model: "model", ModelVersion: "revision", SetupHash: "setup", Scenario: "fix", Repeat: 1, DatasetHash: "fixture", OK: &ok, DurationMS: 200, Checks: checks, Usage: eval.ExternalUsage{Direct: eval.TokenUsage{Input: 200}}}
	write := func(name string, value any) string {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	var out, stderr bytes.Buffer
	code := evalCommand(context.Background(), []string{"compare", "--kiln", write("kiln.jsonl", kiln), "--external", write("external.jsonl", external), "--format", "json"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	var report eval.Comparison
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Pairs) != 1 || report.PairedKnownCostCount != 0 || report.ExternalMeanPairedCostUSD != nil || report.KilnMeanScore != 1 {
		t.Fatalf("report: %+v", report)
	}
}
