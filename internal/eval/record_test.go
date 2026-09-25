package eval

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/andrepato/harness/internal/msg"
)

func TestComputeScore(t *testing.T) {
	tests := []struct {
		name   string
		checks []CheckResult
		judge  *JudgeResult
		want   float64
	}{
		{"checks only, all pass", []CheckResult{{Pass: true}, {Pass: true}}, nil, 1.0},
		{"checks only, half pass", []CheckResult{{Pass: true}, {Pass: false}}, nil, 0.5},
		{"judge only", nil, &JudgeResult{Score: 8}, 0.8},
		{"checks and judge", []CheckResult{{Pass: true}, {Pass: false}}, &JudgeResult{Score: 10}, 0.7*0.5 + 0.3*1.0},
		{"skipped checks excluded", []CheckResult{{Pass: true}, {Skipped: true}}, nil, 1.0},
		{"nothing", nil, nil, 0},
		{"all skipped, no judge", []CheckResult{{Skipped: true}}, nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeScore(tt.checks, tt.judge)
			if diff := got - tt.want; diff > 1e-9 || diff < -1e-9 {
				t.Fatalf("ComputeScore = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRecordRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "results.jsonl")

	records := []Record{
		{
			RunID: "abc", Scenario: "fix-bug", Provider: "faux", Model: "faux-1",
			Repeat: 1, OK: true, Score: 0.9,
			Usage:  msg.Usage{Input: 100, Output: 20},
			Checks: []CheckResult{{Name: "file_matches", Type: "file_matches", Pass: true}},
			Judge:  &JudgeResult{Score: 9, Reasons: []string{"good"}, Model: "faux-verdict"},
		},
		{
			RunID: "abc", Scenario: "fix-bug", Provider: "faux", Model: "faux-2",
			Repeat: 1, OK: false, Score: 0.1, Reason: "timeout",
		},
	}

	if err := WriteRecords(path, records); err != nil {
		t.Fatalf("WriteRecords: %v", err)
	}
	if err := WriteRecords(path, records[1:]); err != nil { // append
		t.Fatalf("WriteRecords append: %v", err)
	}

	got, err := ReadRecords(path)
	if err != nil {
		t.Fatalf("ReadRecords: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d records, want 3", len(got))
	}
	if got[0].Scenario != "fix-bug" || got[0].Model != "faux-1" || !got[0].OK {
		t.Fatalf("unexpected first record: %+v", got[0])
	}
	if got[0].Judge == nil || got[0].Judge.Score != 9 {
		t.Fatalf("judge did not round-trip: %+v", got[0].Judge)
	}
	if got[1].Reason != "timeout" {
		t.Fatalf("reason did not round-trip: %+v", got[1])
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("results file missing: %v", err)
	}
}
