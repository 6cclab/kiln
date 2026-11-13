package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runContextPrint(t *testing.T, args Args, prompt string) string {
	t.Helper()
	args.Print, args.PrintPrompt, args.OutputFormat = true, prompt, "text"
	var out, stderr bytes.Buffer
	if code := Run(context.Background(), args, &out, &stderr, strings.NewReader("")); code != 0 {
		t.Fatalf("Run(%q) exit %d: %s", prompt, code, stderr.String())
	}
	return out.String()
}

func writeContextSkill(t *testing.T, project string) {
	t.Helper()
	dir := filepath.Join(project, ".claude", "skills", "review")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: review\ndescription: Review code\n---\nSECRET REVIEW BODY"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestContextSelectionLiveRestoreAndCompaction(t *testing.T) {
	srv := startFaux(t, `model: faux-1
steps:
  - tool_call: {name: skill, args: {skill: review}, id: skill-1}
  - on_tool_result: skill-1
    then:
      - text: "First turn done."
        end_turn: true
  - text: "Second turn done."
    end_turn: true
  - text: "First task summarized."
    end_turn: true
  - text: "Third turn done."
    end_turn: true
`)
	project := scratchProject(t)
	writeContextSkill(t, project)
	args := baseArgs()
	if out := runContextPrint(t, args, "review this"); !strings.Contains(out, "First turn done") {
		t.Fatal(out)
	}
	args.ContinueLatest = true
	if out := runContextPrint(t, args, "second turn"); !strings.Contains(out, "Second turn done") {
		t.Fatal(out)
	}
	before := runContextPrint(t, args, "/context")
	if !strings.Contains(before, "review skill · body included · exact-name invocation") {
		t.Fatalf("restored context omitted committed skill body:\n%s", before)
	}
	beforeRequests := srv.Requests()
	if len(beforeRequests) < 3 || !strings.Contains(string(beforeRequests[len(beforeRequests)-1].Messages), "SECRET REVIEW BODY") {
		t.Fatalf("second request did not carry the skill body: %+v", beforeRequests)
	}
	if out := runContextPrint(t, args, "/compact"); !strings.Contains(out, "Context compacted") {
		t.Fatalf("compaction did not run: %s", out)
	}
	if out := runContextPrint(t, args, "third turn"); !strings.Contains(out, "Third turn done") {
		t.Fatal(out)
	}
	after := runContextPrint(t, args, "/context")
	if strings.Contains(after, "review skill · body included") {
		t.Fatalf("compacted body reported as current:\n%s", after)
	}
	reqs := srv.Requests()
	if len(reqs) < 5 {
		t.Fatalf("requests = %d, want skill, continuation, second, compaction, third", len(reqs))
	}
	last := string(reqs[len(reqs)-1].Messages)
	if strings.Contains(last, "SECRET REVIEW BODY") {
		t.Fatalf("compacted skill body remained in request: %s", last)
	}
}

func TestContextSelectionLiveCatalogAndClaudeMemory(t *testing.T) {
	srv := startFaux(t, `model: faux-1
steps:
  - text: "Catalog turn done."
    end_turn: true
`)
	project := scratchProject(t)
	writeContextSkill(t, project)
	if err := os.WriteFile(filepath.Join(project, "CLAUDE.md"), []byte("PROJECT INSTRUCTION MARKER"), 0600); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(os.Getenv("HOME"), ".claude", "settings.json")
	autoDir := filepath.Join(t.TempDir(), "notes")
	if err := os.MkdirAll(autoDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(autoDir, "MEMORY.md"), []byte("AUTO MEMORY MARKER"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"skillDiscovery":{"mode":"catalog"},"autoMemoryDirectory":"`+autoDir+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	args := baseArgs()
	if out := runContextPrint(t, args, "unrelated task"); !strings.Contains(out, "Catalog turn done") {
		t.Fatal(out)
	}
	reqs := srv.Requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	system := reqs[0].System
	for _, want := range []string{"<name>review</name>", "PROJECT INSTRUCTION MARKER", "AUTO MEMORY MARKER"} {
		if !strings.Contains(system, want) {
			t.Errorf("request system prompt missing %q", want)
		}
	}
	args.ContinueLatest = true
	report := runContextPrint(t, args, "/context")
	for _, want := range []string{"review skill · metadata included · catalog listing", "Claude instructions", "Claude auto memory"} {
		if !strings.Contains(report, want) {
			t.Errorf("context omitted %q:\n%s", want, report)
		}
	}
}

func TestContextSettingsLiveResumedModelSource(t *testing.T) {
	startFaux(t, `model: faux-2
steps:
  - text: "Seeded model two."
    end_turn: true
`)
	project := scratchProject(t)
	path := filepath.Join(project, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"model":"faux/faux-1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	args := baseArgs()
	args.Model = "faux/faux-2"
	if out := runContextPrint(t, args, "seed session"); !strings.Contains(out, "Seeded model two") {
		t.Fatal(out)
	}
	args.Model = ""
	args.ContinueLatest = true
	contextOut := runContextPrint(t, args, "/context")
	if !strings.Contains(contextOut, "model: faux/faux-2 · from the resumed session") {
		t.Fatal(contextOut)
	}
	var configOut, stderr bytes.Buffer
	if code := EfficiencyConfig(args, &configOut, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	var report struct {
		Settings map[string]any    `json:"contextSettings"`
		Sources  map[string]string `json:"sources"`
	}
	if err := json.Unmarshal(configOut.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Settings["model"] != "faux/faux-2" || report.Sources["model"] != "resumed session" {
		t.Fatal(configOut.String())
	}
}
