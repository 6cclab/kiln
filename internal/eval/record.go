// Package eval is kiln's own evaluation runner: it drives the built kiln
// binary against a directory of scenarios (internal/testkit/scenario),
// grades each run with mechanical Checks and/or an LLM judge, and writes
// one JSONL record per run so results can be compared across models and
// over time (report.go).
package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/andrepato/harness/internal/msg"
)

// ToolCallsSummary counts a run's tool calls and names them, deduplicated.
type ToolCallsSummary struct {
	Count int      `json:"count"`
	Names []string `json:"names,omitempty"`
}

// SubagentsSummary counts subagent dispatches observed in the run log and
// the models they resolved to.
type SubagentsSummary struct {
	Count  int      `json:"count"`
	Models []string `json:"models,omitempty"`
}

// JudgeResult is the judge's verdict plus which model produced it, folded
// into a Record.
type JudgeResult struct {
	Score   float64  `json:"score,omitempty"`
	Reasons []string `json:"reasons,omitempty"`
	Model   string   `json:"model,omitempty"`
	// Error records why no verdict was produced; a judge failure is a
	// fact about the run, not a reason to pretend the scenario was unjudged.
	Error string `json:"error,omitempty"`
}

// ArtifactPaths locates a run's saved artifacts, when Keep was requested.
type ArtifactPaths struct {
	Session string `json:"session,omitempty"`
	Log     string `json:"log,omitempty"`
	Stdout  string `json:"stdout,omitempty"`
}

// Record is one scenario x model x repeat run, as written to a results
// JSONL file (one Record per line).
type Record struct {
	RunID       string `json:"run_id"`
	Timestamp   string `json:"timestamp"`
	KilnVersion string `json:"kiln_version"`

	Scenario string   `json:"scenario"`
	Tags     []string `json:"tags,omitempty"`

	Provider string            `json:"provider"`
	Model    string            `json:"model"`
	Tier     string            `json:"tier,omitempty"`
	Roles    map[string]string `json:"roles,omitempty"`
	Repeat   int               `json:"repeat"`

	OK         bool  `json:"ok"`
	ExitCode   int   `json:"exit_code"`
	DurationMS int64 `json:"duration_ms"`
	Turns      int   `json:"turns"`

	ToolCalls ToolCallsSummary `json:"tool_calls"`
	Subagents SubagentsSummary `json:"subagents"`
	Usage     msg.Usage        `json:"usage"`
	CostUSD   float64          `json:"cost_usd"`
	Checks    []CheckResult    `json:"checks,omitempty"`
	Judge     *JudgeResult     `json:"judge,omitempty"`
	Score     float64          `json:"score"`
	Artifacts ArtifactPaths    `json:"artifacts,omitempty"`
	Reason    string           `json:"reason,omitempty"`
}

// ComputeScore implements the 0.7*(checks pass ratio) + 0.3*(judge/10)
// blend described by the eval plan. When there are no non-skipped checks,
// the score is judge-only; when there is no judge, it is checks-only; when
// neither is present, the score is 0.
func ComputeScore(checks []CheckResult, judge *JudgeResult) float64 {
	// A judge that failed to run produced no judgment: score on checks
	// alone rather than counting the failure as a zero verdict.
	if judge != nil && judge.Error != "" {
		judge = nil
	}
	total, passed := 0, 0
	for _, c := range checks {
		if c.Skipped {
			continue
		}
		total++
		if c.Pass {
			passed++
		}
	}

	hasChecks := total > 0
	hasJudge := judge != nil

	var checkScore, judgeScore float64
	if hasChecks {
		checkScore = float64(passed) / float64(total)
	}
	if hasJudge {
		judgeScore = judge.Score / 10
	}

	switch {
	case hasChecks && hasJudge:
		return 0.7*checkScore + 0.3*judgeScore
	case hasChecks:
		return checkScore
	case hasJudge:
		return judgeScore
	default:
		return 0
	}
}

// WriteRecords appends records to path as JSONL, creating it (and its
// parent directory) if necessary.
func WriteRecords(path string, records []Record) error {
	if err := os.MkdirAll(dirOf(path), 0o755); err != nil {
		return fmt.Errorf("eval: mkdir for %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("eval: open %s: %w", path, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			return fmt.Errorf("eval: encode record: %w", err)
		}
	}
	return nil
}

// ReadRecords reads every Record from a JSONL file.
func ReadRecords(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("eval: open %s: %w", path, err)
	}
	defer f.Close()

	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("eval: parsing %s: %w", path, err)
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("eval: reading %s: %w", path, err)
	}
	return out, nil
}

func dirOf(path string) string {
	i := len(path) - 1
	for i >= 0 && path[i] != '/' {
		i--
	}
	if i < 0 {
		return "."
	}
	return path[:i]
}
