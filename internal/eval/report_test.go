package eval

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
)

func usageOf(input, output int) msg.Usage {
	return msg.Usage{Input: input, Output: output}
}

func writeResults(t *testing.T, path string, records []Record) {
	t.Helper()
	if err := WriteRecords(path, records); err != nil {
		t.Fatalf("WriteRecords: %v", err)
	}
}

func TestReportRegression(t *testing.T) {
	dir := t.TempDir()
	baseline := filepath.Join(dir, "baseline.jsonl")
	target := filepath.Join(dir, "20260101-000000-abcd1234.jsonl")

	writeResults(t, baseline, []Record{
		{Scenario: "fix-bug", Provider: "faux", Model: "faux-1", Repeat: 1, OK: true, Score: 0.9},
	})
	writeResults(t, target, []Record{
		{Scenario: "fix-bug", Provider: "faux", Model: "faux-1", Repeat: 1, OK: true, Score: 0.5},
	})

	out, code, err := Report(ReportOptions{ResultsDir: dir, FailOnRegression: 0.1, Format: "text"})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (regression)", code)
	}
	if !strings.Contains(out, "fix-bug") {
		t.Fatalf("report missing scenario name:\n%s", out)
	}
	if !strings.Contains(out, "regression") {
		t.Fatalf("report did not flag the regression:\n%s", out)
	}
}

func TestReportNoRegression(t *testing.T) {
	dir := t.TempDir()
	baseline := filepath.Join(dir, "baseline.jsonl")
	target := filepath.Join(dir, "20260101-000000-abcd1234.jsonl")

	writeResults(t, baseline, []Record{
		{Scenario: "fix-bug", Provider: "faux", Model: "faux-1", Repeat: 1, OK: true, Score: 0.9},
	})
	writeResults(t, target, []Record{
		{Scenario: "fix-bug", Provider: "faux", Model: "faux-1", Repeat: 1, OK: true, Score: 0.95},
	})

	_, code, err := Report(ReportOptions{ResultsDir: dir, FailOnRegression: 0.1})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
}

func TestReportMarkdown(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "20260101-000000-abcd1234.jsonl")
	writeResults(t, target, []Record{
		{Scenario: "fix-bug", Provider: "faux", Model: "faux-1", Repeat: 1, OK: true, Score: 1.0,
			Checks: []CheckResult{{Pass: true}, {Pass: true}}, Usage: usageOf(100, 20), CostUSD: 0.01, Turns: 2, DurationMS: 1200},
		{Scenario: "fix-bug", Provider: "faux", Model: "faux-1", Repeat: 2, OK: true, Score: 0.5,
			Checks: []CheckResult{{Pass: true}, {Pass: false}}, Usage: usageOf(110, 22), CostUSD: 0.012, Turns: 3, DurationMS: 1300},
	})

	out, code, err := Report(ReportOptions{ResultsDir: dir, Format: "md"})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out, "## fix-bug") {
		t.Fatalf("markdown report missing scenario heading:\n%s", out)
	}
	if !strings.Contains(out, "faux/faux-1") {
		t.Fatalf("markdown report missing model column:\n%s", out)
	}
	if !strings.Contains(out, "0.750") {
		t.Fatalf("markdown report should average the two repeats' scores to 0.75:\n%s", out)
	}
}
