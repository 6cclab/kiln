package eval

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ReportOptions configures Report.
type ReportOptions struct {
	ResultsDir string
	// Target is the results JSONL file to report on. Defaults to the
	// newest "*.jsonl" file directly under ResultsDir other than
	// baseline.jsonl.
	Target string
	// Baseline is the results JSONL file to diff Target against. Defaults
	// to "<ResultsDir>/baseline.jsonl"; if that file does not exist, the
	// delta section is omitted rather than erroring.
	Baseline string
	// Last, if > 0, keeps only the most recent Last repeats per
	// (scenario, model) pair from Target before aggregating.
	Last int
	// Format is "text" (default), "md" or "json".
	Format string
	// FailOnRegression is the score-drop threshold (0-1) past which
	// Report returns exit code 1. 0 disables the check.
	FailOnRegression float64
}

// pairKey identifies one (scenario, model) group.
type pairKey struct {
	Scenario string
	Model    string
}

func (k pairKey) String() string { return k.Scenario + " / " + k.Model }

// pairStats is the aggregate over a group's repeats.
type pairStats struct {
	N            int
	MeanScore    float64
	StddevScore  float64
	MeanCost     float64
	MeanTurns    float64
	MeanDuration float64
	MeanInput    float64
	MeanOutput   float64
	ChecksPassed int
	ChecksTotal  int
	MeanJudge    float64
	HasJudge     bool
}

func aggregate(records []Record) map[pairKey]pairStats {
	byKey := map[pairKey][]Record{}
	for _, r := range records {
		k := pairKey{Scenario: r.Scenario, Model: r.Provider + "/" + r.Model}
		byKey[k] = append(byKey[k], r)
	}
	out := map[pairKey]pairStats{}
	for k, rs := range byKey {
		out[k] = statsOf(rs)
	}
	return out
}

func statsOf(rs []Record) pairStats {
	n := len(rs)
	var sumScore, sumCost, sumTurns, sumDur, sumIn, sumOut, sumJudge float64
	var judgeN int
	var passed, total int
	scores := make([]float64, 0, n)
	for _, r := range rs {
		sumScore += r.Score
		scores = append(scores, r.Score)
		sumCost += r.CostUSD
		sumTurns += float64(r.Turns)
		sumDur += float64(r.DurationMS)
		sumIn += float64(r.Usage.Input)
		sumOut += float64(r.Usage.Output)
		for _, c := range r.Checks {
			if c.Skipped {
				continue
			}
			total++
			if c.Pass {
				passed++
			}
		}
		if r.Judge != nil {
			sumJudge += r.Judge.Score
			judgeN++
		}
	}
	mean := sumScore / float64(n)
	var variance float64
	for _, s := range scores {
		variance += (s - mean) * (s - mean)
	}
	stddev := 0.0
	if n > 1 {
		stddev = math.Sqrt(variance / float64(n))
	}
	st := pairStats{
		N: n, MeanScore: mean, StddevScore: stddev,
		MeanCost: sumCost / float64(n), MeanTurns: sumTurns / float64(n),
		MeanDuration: sumDur / float64(n), MeanInput: sumIn / float64(n), MeanOutput: sumOut / float64(n),
		ChecksPassed: passed, ChecksTotal: total,
	}
	if judgeN > 0 {
		st.HasJudge = true
		st.MeanJudge = sumJudge / float64(judgeN)
	}
	return st
}

// newestResultsFile returns the lexicographically-newest "*.jsonl" file
// directly under dir, other than "baseline.jsonl". File names are
// timestamp-prefixed (see runner.go), so lexicographic order is
// chronological order.
func newestResultsFile(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("eval: reading %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || e.Name() == "baseline.jsonl" || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		names = append(names, e.Name())
	}
	if len(names) == 0 {
		return "", fmt.Errorf("eval: no results files in %s", dir)
	}
	sort.Strings(names)
	return filepath.Join(dir, names[len(names)-1]), nil
}

func keepLast(records []Record, last int) []Record {
	if last <= 0 {
		return records
	}
	byKey := map[pairKey][]Record{}
	var order []pairKey
	for _, r := range records {
		k := pairKey{Scenario: r.Scenario, Model: r.Provider + "/" + r.Model}
		if _, ok := byKey[k]; !ok {
			order = append(order, k)
		}
		byKey[k] = append(byKey[k], r)
	}
	var out []Record
	for _, k := range order {
		rs := byKey[k]
		if len(rs) > last {
			rs = rs[len(rs)-last:]
		}
		out = append(out, rs...)
	}
	return out
}

// Report renders a comparison of opts.Target against opts.Baseline (when
// present), grouped by (scenario, model). It returns the rendered report,
// an exit code (1 when any pair regressed by more than
// opts.FailOnRegression), and an error for anything that stopped it from
// running at all.
func Report(opts ReportOptions) (string, int, error) {
	target := opts.Target
	if target == "" {
		var err error
		target, err = newestResultsFile(opts.ResultsDir)
		if err != nil {
			return "", 2, err
		}
	}
	targetRecords, err := ReadRecords(target)
	if err != nil {
		return "", 2, err
	}
	targetRecords = keepLast(targetRecords, opts.Last)
	targetStats := aggregate(targetRecords)

	baseline := opts.Baseline
	if baseline == "" {
		baseline = filepath.Join(opts.ResultsDir, "baseline.jsonl")
	}
	var baselineStats map[pairKey]pairStats
	if _, err := os.Stat(baseline); err == nil {
		baselineRecords, err := ReadRecords(baseline)
		if err != nil {
			return "", 2, err
		}
		baselineStats = aggregate(baselineRecords)
	}

	format := opts.Format
	if format == "" {
		format = "text"
	}

	exitCode := 0
	if opts.FailOnRegression > 0 && baselineStats != nil {
		for k, ts := range targetStats {
			if bs, ok := baselineStats[k]; ok {
				if bs.MeanScore-ts.MeanScore > opts.FailOnRegression {
					exitCode = 1
				}
			}
		}
	}

	scenarios := scenarioOrder(targetRecords)

	var out string
	switch format {
	case "json":
		out, err = renderJSON(scenarios, targetStats, baselineStats)
	case "md":
		out = renderTables(scenarios, targetStats, baselineStats, true)
	default:
		out = renderTables(scenarios, targetStats, baselineStats, false)
	}
	if err != nil {
		return "", 2, err
	}
	return out, exitCode, nil
}

func scenarioOrder(records []Record) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range records {
		if !seen[r.Scenario] {
			seen[r.Scenario] = true
			out = append(out, r.Scenario)
		}
	}
	sort.Strings(out)
	return out
}

func modelsFor(scenario string, stats map[pairKey]pairStats) []string {
	var out []string
	for k := range stats {
		if k.Scenario == scenario {
			out = append(out, k.Model)
		}
	}
	sort.Strings(out)
	return out
}

func renderTables(scenarios []string, target, baseline map[pairKey]pairStats, md bool) string {
	var b strings.Builder
	for _, sc := range scenarios {
		models := modelsFor(sc, target)
		if md {
			fmt.Fprintf(&b, "## %s\n\n", sc)
			b.WriteString("| metric |")
			for _, m := range models {
				fmt.Fprintf(&b, " %s |", m)
			}
			b.WriteString("\n|---|")
			for range models {
				b.WriteString("---|")
			}
			b.WriteString("\n")
		} else {
			fmt.Fprintf(&b, "=== %s ===\n", sc)
		}
		rows := []string{"score", "checks", "judge", "tokens in", "tokens out", "cost", "turns", "duration"}
		for _, row := range rows {
			if md {
				fmt.Fprintf(&b, "| %s |", row)
			} else {
				fmt.Fprintf(&b, "%-12s", row)
			}
			for _, m := range models {
				st := target[pairKey{Scenario: sc, Model: m}]
				val := cellValue(row, st)
				if md {
					fmt.Fprintf(&b, " %s |", val)
				} else {
					fmt.Fprintf(&b, " %-16s", val)
				}
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")

		if baseline != nil {
			if md {
				fmt.Fprintf(&b, "### %s: delta vs baseline\n\n", sc)
				b.WriteString("| model | baseline score | target score | delta |\n|---|---|---|---|\n")
			} else {
				fmt.Fprintf(&b, "--- %s: delta vs baseline ---\n", sc)
			}
			for _, m := range models {
				k := pairKey{Scenario: sc, Model: m}
				ts := target[k]
				bs, ok := baseline[k]
				if !ok {
					continue
				}
				delta := ts.MeanScore - bs.MeanScore
				flag := ""
				if delta < 0 {
					flag = " (regression)"
				}
				if md {
					fmt.Fprintf(&b, "| %s | %.3f | %.3f | %+.3f%s |\n", m, bs.MeanScore, ts.MeanScore, delta, flag)
				} else {
					fmt.Fprintf(&b, "%-20s baseline=%.3f target=%.3f delta=%+.3f%s\n", m, bs.MeanScore, ts.MeanScore, delta, flag)
				}
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func cellValue(row string, st pairStats) string {
	switch row {
	case "score":
		if st.StddevScore > 0 {
			return fmt.Sprintf("%.3f ±%.3f", st.MeanScore, st.StddevScore)
		}
		return fmt.Sprintf("%.3f", st.MeanScore)
	case "checks":
		return fmt.Sprintf("%d/%d", st.ChecksPassed, st.ChecksTotal)
	case "judge":
		if !st.HasJudge {
			return "-"
		}
		return fmt.Sprintf("%.1f/10", st.MeanJudge)
	case "tokens in":
		return fmt.Sprintf("%.0f", st.MeanInput)
	case "tokens out":
		return fmt.Sprintf("%.0f", st.MeanOutput)
	case "cost":
		return fmt.Sprintf("$%.4f", st.MeanCost)
	case "turns":
		return fmt.Sprintf("%.1f", st.MeanTurns)
	case "duration":
		return fmt.Sprintf("%.0fms", st.MeanDuration)
	default:
		return ""
	}
}

// jsonPair is Report's json output shape for one (scenario, model) group.
type jsonPair struct {
	Scenario string     `json:"scenario"`
	Model    string     `json:"model"`
	Target   pairStats  `json:"target"`
	Baseline *pairStats `json:"baseline,omitempty"`
	Delta    *float64   `json:"score_delta,omitempty"`
}

func renderJSON(scenarios []string, target, baseline map[pairKey]pairStats) (string, error) {
	var out []jsonPair
	for _, sc := range scenarios {
		for _, m := range modelsFor(sc, target) {
			k := pairKey{Scenario: sc, Model: m}
			jp := jsonPair{Scenario: sc, Model: m, Target: target[k]}
			if baseline != nil {
				if bs, ok := baseline[k]; ok {
					bs := bs
					jp.Baseline = &bs
					delta := jp.Target.MeanScore - bs.MeanScore
					jp.Delta = &delta
				}
			}
			out = append(out, jp)
		}
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
