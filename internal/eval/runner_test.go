package eval

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

var kilnBin string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "kiln-eval-bin-")
	if err != nil {
		panic("eval test: mkdtemp: " + err.Error())
	}
	defer os.RemoveAll(tmp)

	kilnBin = filepath.Join(tmp, "kiln")
	if out, err := exec.Command("go", "build", "-o", kilnBin, "github.com/andrepato/harness/cmd/kiln").CombinedOutput(); err != nil {
		panic("eval test: build cmd/kiln: " + err.Error() + "\n" + string(out))
	}

	os.Exit(m.Run())
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const tinyFauxScript = `model: faux-1
steps:
  - text: "I'll look at the file."
  - tool_call: {name: read, args: {path: src/math.js}, id: tc1}
  - on_tool_result: tc1
    then:
      - tool_call:
          name: edit
          args:
            path: src/math.js
            edits:
              - oldText: "return a - b;"
                newText: "return a + b;"
          id: tc2
  - on_tool_result: tc2
    then:
      - text: "Fixed."
        usage: {input: 200, output: 20}
`

const tinyScenarioYAML = `name: tiny-fix
description: fixes a trivial bug, for internal/eval's own tests
tags: [smoke]
fixture: tinymath
prompt: fix the bug in math.js so add() adds
permission_mode: acceptEdits
allowed_tools: [read, edit]
caps:
  max_turns: 5
checks:
  - type: file_matches
    path: src/math.js
    regex: 'return a \+ b'
  - type: result_ok
  - type: tool_calls
    max: 6
judge:
  rubric: "Did the agent fix add() without touching mul()?"
  faux_verdict: {score: 9, reasons: ["fixed cleanly"]}
faux:
  anthropic-messages: faux.yaml
`

const tinyMathJS = "function add(a, b) {\n  return a - b;\n}\n\nfunction mul(a, b) {\n  return a * b;\n}\n"

func setupTinyScenario(t *testing.T) (scenariosDir, fixturesDir string) {
	t.Helper()
	dir := t.TempDir()
	scenariosDir = filepath.Join(dir, "scenarios")
	fixturesDir = filepath.Join(dir, "fixtures")
	writeFile(t, filepath.Join(scenariosDir, "tiny-fix", "scenario.yaml"), tinyScenarioYAML)
	writeFile(t, filepath.Join(scenariosDir, "tiny-fix", "faux.yaml"), tinyFauxScript)
	writeFile(t, filepath.Join(fixturesDir, "tinymath", "src", "math.js"), tinyMathJS)
	return scenariosDir, fixturesDir
}

func TestRunEndToEndFaux(t *testing.T) {
	scenariosDir, fixturesDir := setupTinyScenario(t)
	resultsDir := t.TempDir()

	summary, err := Run(context.Background(), RunOptions{
		KilnBin:      kilnBin,
		ScenariosDir: scenariosDir,
		FixturesDir:  fixturesDir,
		ResultsDir:   resultsDir,
		Models:       []ModelConfig{{Provider: "faux", Model: "faux-1", Faux: true}},
		Repeat:       1,
		Parallel:     2,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(summary.Records) != 1 {
		t.Fatalf("got %d records, want 1", len(summary.Records))
	}
	r := summary.Records[0]
	if !r.OK {
		t.Fatalf("record not ok: reason=%q checks=%+v", r.Reason, r.Checks)
	}
	if r.Scenario != "tiny-fix" || r.Provider != "faux" || r.Model != "faux-1" {
		t.Fatalf("unexpected record identity: %+v", r)
	}
	for _, c := range r.Checks {
		if !c.Pass && !c.Skipped {
			t.Errorf("check %s (%s) failed: %s", c.Name, c.Type, c.Detail)
		}
	}
	if r.Judge == nil || r.Judge.Score != 9 {
		t.Fatalf("expected faux_verdict judge score 9, got %+v", r.Judge)
	}
	if r.Score <= 0 {
		t.Fatalf("expected a positive score, got %v", r.Score)
	}
	if r.Tier == "" {
		t.Errorf("expected a tier to be resolved for faux-1")
	}

	if _, err := os.Stat(summary.ResultsFile); err != nil {
		t.Fatalf("results file missing: %v", err)
	}
	fromDisk, err := ReadRecords(summary.ResultsFile)
	if err != nil {
		t.Fatalf("ReadRecords: %v", err)
	}
	if len(fromDisk) != 1 {
		t.Fatalf("got %d records on disk, want 1", len(fromDisk))
	}
}

func TestRunRefusesLiveModelsWithoutOptIn(t *testing.T) {
	scenariosDir, fixturesDir := setupTinyScenario(t)
	resultsDir := t.TempDir()

	t.Setenv("KILN_EVAL_LIVE", "")
	_, err := Run(context.Background(), RunOptions{
		KilnBin:      kilnBin,
		ScenariosDir: scenariosDir,
		FixturesDir:  fixturesDir,
		ResultsDir:   resultsDir,
		Models:       []ModelConfig{{Provider: "anthropic", Model: "claude-x"}},
	})
	if err == nil {
		t.Fatal("expected an error refusing the live model")
	}
	if entries, _ := os.ReadDir(resultsDir); len(entries) != 0 {
		t.Fatalf("expected no results written before refusing, got %v", entries)
	}
}

func TestRunKeepArtifacts(t *testing.T) {
	scenariosDir, fixturesDir := setupTinyScenario(t)
	resultsDir := t.TempDir()

	summary, err := Run(context.Background(), RunOptions{
		KilnBin:      kilnBin,
		ScenariosDir: scenariosDir,
		FixturesDir:  fixturesDir,
		ResultsDir:   resultsDir,
		Models:       []ModelConfig{{Provider: "faux", Model: "faux-1", Faux: true}},
		Repeat:       1,
		Parallel:     1,
		Keep:         true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	dest := filepath.Join(resultsDir, "runs", summary.Records[0].RunID, "tiny-fix", "faux-faux-1", "1")
	if _, err := os.Stat(filepath.Join(dest, "stdout.json")); err != nil {
		t.Fatalf("expected kept stdout.json at %s: %v", dest, err)
	}
}
