// kiln eval: dispatch for `kiln eval run` and `kiln eval report`, the
// evaluation runner's CLI surface. Kept out of main.go (dispatch only) and
// out of internal/cli (which owns the chat/print/management commands);
// this file only parses flags and calls internal/eval.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/eval"
)

func evalCommand(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	if len(argv) == 0 {
		fmt.Fprintln(stderr, "usage: kiln eval <run|report> [flags]")
		return 2
	}
	switch argv[0] {
	case "run":
		return evalRun(ctx, argv[1:], stdout, stderr)
	case "report":
		return evalReport(ctx, argv[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "kiln eval: unknown subcommand %q (want run or report)\n", argv[0])
		return 2
	}
}

func defaultKilnBin() string {
	if b := eval.DefaultKilnBin(); b != "" {
		return b
	}
	return "bin/kiln"
}

func defaultRoles() map[string]string {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	s := claudesettings.LoadSettings(cwd, claudesettings.LoadOptions{})
	return s.ModelRoles
}

// parseRoles accepts either a path to a JSON file of {role: "provider/model"}
// or a comma-separated k=v list ("fast=faux/faux-2,heavy=faux/faux-1").
func parseRoles(spec string) (map[string]string, error) {
	if spec == "" {
		return defaultRoles(), nil
	}
	if strings.Contains(spec, "=") {
		out := map[string]string{}
		for _, pair := range strings.Split(spec, ",") {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			kv := strings.SplitN(pair, "=", 2)
			if len(kv) != 2 {
				return nil, fmt.Errorf("--roles: invalid entry %q, want role=provider/model", pair)
			}
			out[kv[0]] = kv[1]
		}
		return out, nil
	}
	data, err := os.ReadFile(spec)
	if err != nil {
		return nil, fmt.Errorf("--roles: reading %s: %w", spec, err)
	}
	var out map[string]string
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("--roles: parsing %s: %w", spec, err)
	}
	return out, nil
}

func evalRun(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("kiln eval run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	kilnBin := fs.String("kiln-bin", defaultKilnBin(), "path to the kiln binary to evaluate")
	scenariosDir := fs.String("scenarios", "eval/scenarios", "directory of scenario subdirectories")
	fixturesDir := fs.String("fixtures", "eval/fixtures", "directory of fixture subdirectories")
	resultsDir := fs.String("results", "eval/results", "directory to write results JSONL into")
	modelsFlag := fs.String("models", "faux/faux-1", "comma-separated provider/model list")
	rolesFlag := fs.String("roles", "", "role file (JSON) or k=v,k=v list; defaults to this machine's own modelRoles")
	repeat := fs.Int("repeat", 1, "repeats per (scenario, model)")
	parallel := fs.Int("j", defaultParallel(), "parallel workers")
	keep := fs.Bool("keep", false, "keep per-run artifacts under <results>/runs/")
	judge := fs.String("judge", "", "provider/model to grade live runs against a scenario's judge rubric")
	only := fs.String("only", "", "comma-separated scenario name/tag filter")
	if err := fs.Parse(argv); err != nil {
		return 2
	}

	var models []eval.ModelConfig
	for _, m := range strings.Split(*modelsFlag, ",") {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		mc, err := eval.ParseModelConfig(m)
		if err != nil {
			fmt.Fprintln(stderr, "kiln eval run:", err)
			return 2
		}
		models = append(models, mc)
	}

	roles, err := parseRoles(*rolesFlag)
	if err != nil {
		fmt.Fprintln(stderr, "kiln eval run:", err)
		return 2
	}

	var filter []string
	for _, f := range strings.Split(*only, ",") {
		f = strings.TrimSpace(f)
		if f != "" {
			filter = append(filter, f)
		}
	}

	// Live runs are judged by the evaluator's own "fast" role unless a
	// judge is named: the plan's default, and the cheapest model configured.
	if *judge == "" {
		if fast := roles["fast"]; fast != "" {
			*judge = fast
			fmt.Fprintf(stderr, "kiln eval run: judging live runs with %s (the fast role)\n", fast)
		}
	}

	summary, err := eval.Run(ctx, eval.RunOptions{
		KilnBin:      *kilnBin,
		ScenariosDir: *scenariosDir,
		FixturesDir:  *fixturesDir,
		ResultsDir:   *resultsDir,
		Models:       models,
		Roles:        roles,
		Repeat:       *repeat,
		Parallel:     *parallel,
		Keep:         *keep,
		Judge:        *judge,
		Filter:       filter,
	})
	if err != nil {
		fmt.Fprintln(stderr, "kiln eval run:", err)
		return 1
	}

	fmt.Fprintf(stdout, "kiln eval run: %d record(s) written to %s (kiln %s)\n", len(summary.Records), summary.ResultsFile, summary.KilnVersion)
	if summary.Failed > 0 {
		fmt.Fprintf(stdout, "%d of %d run(s) did not complete ok\n", summary.Failed, len(summary.Records))
	}
	return 0
}

func defaultParallel() int {
	n := runtime.NumCPU()
	if n < 1 {
		return 1
	}
	if n > 8 {
		return 8
	}
	return n
}

func evalReport(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("kiln eval report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	resultsDir := fs.String("results", "eval/results", "directory results JSONL files live in")
	target := fs.String("target", "", "results JSONL file to report on (default: newest under --results)")
	baseline := fs.String("baseline", "", "results JSONL file to diff against (default: <results>/baseline.jsonl)")
	last := fs.Int("last", 0, "keep only the last N repeats per (scenario, model) pair")
	format := fs.String("format", "text", "output format: text, md or json")
	failOnRegression := fs.Float64("fail-on-regression", 0, "exit 1 when any pair's score drops by more than this")
	if err := fs.Parse(argv); err != nil {
		return 2
	}

	out, code, err := eval.Report(eval.ReportOptions{
		ResultsDir:       *resultsDir,
		Target:           *target,
		Baseline:         *baseline,
		Last:             *last,
		Format:           *format,
		FailOnRegression: *failOnRegression,
	})
	if err != nil {
		fmt.Fprintln(stderr, "kiln eval report:", err)
		return 2
	}
	fmt.Fprint(stdout, out)
	return code
}
