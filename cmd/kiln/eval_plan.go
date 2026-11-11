package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/andrepato/harness/internal/diag"
	"github.com/andrepato/harness/internal/eval"
	"github.com/andrepato/harness/internal/learning"
)

func comparePlannedFiles(planPath, kilnPath, externalPath string) (eval.Comparison, error) {
	if planPath == "" {
		return eval.CompareFiles(kilnPath, externalPath)
	}
	f, err := os.Open(planPath)
	if err != nil {
		return eval.Comparison{}, err
	}
	defer diag.Close(f)
	dec := json.NewDecoder(io.LimitReader(f, 1<<20))
	dec.DisallowUnknownFields()
	var plan eval.BenchmarkPlan
	if err := dec.Decode(&plan); err != nil {
		return eval.Comparison{}, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return eval.Comparison{}, fmt.Errorf("benchmark plan must contain one JSON object")
	}
	kiln, err := eval.ReadRecords(kilnPath)
	if err != nil {
		return eval.Comparison{}, err
	}
	external, err := eval.ReadExternalRecords(externalPath)
	if err != nil {
		return eval.Comparison{}, err
	}
	return eval.ValidateBenchmarkResults(plan, kiln, external)
}

// evalPlan records local inputs and makes no provider requests.
func evalPlan(argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("kiln eval plan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	kilnBin := fs.String("kiln-bin", defaultKilnBin(), "kiln binary to fingerprint")
	claudeBin := fs.String("claude-bin", "claude", "Claude Code binary to fingerprint")
	scenarios := fs.String("scenarios", "eval/scenarios", "scenario directory")
	fixtures := fs.String("fixtures", "eval/fixtures", "fixture directory")
	claudeSettings := fs.String("claude-settings", "", "explicit Claude settings file to fingerprint")
	kilnModel := fs.String("kiln-model", "", "exact provider/model ID")
	claudeModel := fs.String("claude-model", "", "exact Claude model ID")
	rolesFlag := fs.String("roles", "", "explicit role file or k=v list; empty means no role overrides")
	policyFile := fs.String("policy", "", "kiln efficiency policy to fingerprint")
	names := fs.String("only-exact", "", "exact scenario names; default is the four-task comparison cohort")
	repeats := fs.Int("repeat", 2, "repeats per scenario and harness (2..20)")
	maxUSD := fs.Float64("max-usd", 0, "planned total USD allowance; this offline command does not enforce spending")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		evalDiagnostic(stderr, "kiln eval plan: unexpected arguments")
		return 2
	}
	var roles map[string]string
	var err error
	if *rolesFlag != "" {
		roles, err = parseRoles(*rolesFlag)
		if err != nil {
			evalDiagnostic(stderr, "kiln eval plan:", err)
			return 2
		}
	}
	policyHash := ""
	if *policyFile != "" {
		policy, err := learning.ReadPolicy(*policyFile)
		if err != nil {
			evalDiagnostic(stderr, "kiln eval plan:", err)
			return 2
		}
		policyHash = policy.Hash()
	}
	var selected []string
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "only-exact" {
			selected = strings.Split(*names, ",")
		}
	})
	resolvedClaude, err := exec.LookPath(*claudeBin)
	if err != nil {
		evalDiagnostic(stderr, "kiln eval plan:", err)
		return 2
	}
	plan, err := eval.BuildBenchmarkPlan(eval.BenchmarkPlanOptions{
		KilnBin: *kilnBin, ClaudeBin: resolvedClaude, ScenariosDir: *scenarios, FixturesDir: *fixtures,
		ClaudeSettings: *claudeSettings, KilnModel: *kilnModel, ClaudeModel: *claudeModel,
		KilnRoles: roles, KilnPolicyHash: policyHash, ScenarioNames: selected, Repeats: *repeats, MaxTotalUSD: *maxUSD,
	})
	if err != nil {
		evalDiagnostic(stderr, "kiln eval plan:", err)
		return 2
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(plan); err != nil {
		evalDiagnostic(stderr, "kiln eval plan:", err)
		return 1
	}
	return 0
}
