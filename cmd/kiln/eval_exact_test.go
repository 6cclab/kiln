package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/eval"
)

func TestEvalRunOnlyExactCLI(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "kiln")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build kiln: %v\n%s", err, out)
	}
	scenarios := filepath.Join(root, "scenarios")
	fixtures := filepath.Join(root, "fixtures")
	put := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"alpha", "alpha-long"} {
		dir := filepath.Join(scenarios, name)
		put(filepath.Join(dir, "scenario.yaml"), "name: "+name+"\ndescription: exact selection fixture\nfixture: tiny\nprompt: say done\nchecks:\n  - type: result_ok\nfaux:\n  anthropic-messages: faux.yaml\n")
		put(filepath.Join(dir, "faux.yaml"), "model: faux-1\nsteps:\n  - text: done\n")
	}
	for name, tags := range map[string]string{
		"bad-pin":    "[pin-model:invalid]",
		"double-pin": "[pin-model:faux/faux-1, pin-model:faux/faux-2]",
	} {
		dir := filepath.Join(scenarios, name)
		put(filepath.Join(dir, "scenario.yaml"), "name: "+name+"\ndescription: invalid model pin fixture\ntags: "+tags+"\nfixture: tiny\nprompt: say done\nchecks:\n  - type: result_ok\nfaux:\n  anthropic-messages: faux.yaml\n")
		put(filepath.Join(dir, "faux.yaml"), "model: faux-1\nsteps:\n  - text: done\n")
	}
	put(filepath.Join(fixtures, "tiny", "README.txt"), "fixture")
	invoke := func(results string, extra ...string) (int, string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		args := []string{"eval", "run", "--kiln-bin", bin, "--scenarios", scenarios, "--fixtures", fixtures, "--results", results, "--models", "faux/faux-1", "--roles", "fast=faux/faux-1", "--j", "1"}
		args = append(args, extra...)
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = append(os.Environ(), "HOME="+filepath.Join(root, "home"), "HARNESS_OFFLINE=1", "KILN_EVAL_LIVE=0")
		out, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			t.Fatalf("eval timed out: %v\n%s", ctx.Err(), out)
		}
		if err == nil {
			return 0, string(out)
		}
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode(), string(out)
		}
		t.Fatalf("exec eval: %v\n%s", err, out)
		return -1, ""
	}

	selectedDir := filepath.Join(root, "selected")
	code, output := invoke(selectedDir, "--only-exact", "alpha")
	if code != 0 {
		t.Fatalf("exact selection exit=%d: %s", code, output)
	}
	entries, err := os.ReadDir(selectedDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("results files=%v err=%v", entries, err)
	}
	f, err := os.Open(filepath.Join(selectedDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	}()
	scan := bufio.NewScanner(f)
	var records []eval.Record
	for scan.Scan() {
		var record eval.Record
		if err := json.Unmarshal(scan.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Scenario != "alpha" {
		t.Fatalf("prefix collision selected wrong scenarios: %+v", records)
	}

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "missing", args: []string{"--only-exact", "alpha-missing"}, want: "not found"},
		{name: "mutual exclusion", args: []string{"--only", "alpha", "--only-exact", "alpha"}, want: "mutually exclusive"},
		{name: "empty exact", args: []string{"--only-exact="}, want: "only-exact"},
		{name: "whitespace exact", args: []string{"--only-exact", "  ,  "}, want: "only-exact"},
		{name: "malformed model pin", args: []string{"--only-exact", "bad-pin"}, want: "invalid model pin"},
		{name: "multiple model pins", args: []string{"--only-exact", "double-pin"}, want: "multiple model pins"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			results := filepath.Join(root, strings.ReplaceAll(tc.name, " ", "-"))
			code, output := invoke(results, tc.args...)
			if code == 0 || !strings.Contains(output, tc.want) {
				t.Fatalf("exit=%d output=%s, want %q", code, output, tc.want)
			}
			entries, err := os.ReadDir(results)
			if err == nil && len(entries) != 0 {
				t.Fatalf("invalid selection started a run: %v", entries)
			}
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
		})
	}
}
