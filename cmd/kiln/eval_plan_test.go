package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/andrepato/harness/internal/eval"
)

func TestEvalPlanOfflineCohort(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-harness")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n[ \"$1\" = --version ] || exit 91\nprintf 'test-version\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	args := []string{"plan", "--kiln-bin", bin, "--claude-bin", bin, "--scenarios", "../../eval/scenarios", "--fixtures", "../../eval/fixtures", "--kiln-model", "anthropic/claude-opus-4-8", "--claude-model", "claude-opus-4-8", "--max-usd", "25"}
	var out, stderr bytes.Buffer
	if code := evalCommand(context.Background(), args, &out, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, &stderr)
	}
	var plan eval.BenchmarkPlan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.TotalPairs != 8 || plan.TotalRuns != 16 || plan.MaxTotalUSD != 25 || plan.KilnBinaryHash == "" || plan.ClaudeSetupHash == "" || len(plan.KilnRoles) != 0 {
		t.Fatalf("invalid plan: %+v", plan)
	}
	for _, flags := range [][]string{{"--only-exact", ""}, {"--only-exact", "fix-bug,missing"}, {"--repeat", "1"}, {"--max-usd", "0"}, {"--claude-model", "opus"}, {"extra"}} {
		out.Reset()
		stderr.Reset()
		if code := evalCommand(context.Background(), append(append([]string{}, args...), flags...), &out, &stderr); code != 2 {
			t.Fatalf("%v exit %d: %s", flags, code, &stderr)
		}
	}
}

func TestEvalCompareRejectsInvalidPlanBeforeResults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(p, []byte("{} {}"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := evalCommand(context.Background(), []string{"compare", "--plan", p, "--kiln", "unused", "--external", "unused"}, &out, &stderr); code != 2 || !bytes.Contains(stderr.Bytes(), []byte("one JSON object")) {
		t.Fatalf("exit %d: %s", code, &stderr)
	}
}
