//go:build e2e

// live_test.go is the Go port of scripts/e2e.ts: drive a real model (no
// faux) through a scratch project with a deliberate bug and check it gets
// fixed. Gated behind HARNESS_E2E_LIVE=1 (and, in `make e2e-live`, -run
// Live) so a plain `make e2e` never touches the network or waits on a
// model load.
package e2e

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// firstToolCapableOllamaModel runs `harness models ollama` and returns the
// first model id it lists. ollama.New defaults RequireTools to true (see
// internal/provider/ollama/ollama.go), so every model this lists is
// already tool-capable; nothing extra to filter here.
func firstToolCapableOllamaModel(t *testing.T, home string) string {
	t.Helper()
	res := runBinary(t, harnessBin, home, map[string]string{
		"HOME":        home,
		"OLLAMA_HOST": os.Getenv("OLLAMA_HOST"),
	}, 30*time.Second, "models", "ollama")
	if res.Code != 0 {
		t.Fatalf("harness models ollama: exit code %d, stderr=%s", res.Code, res.Stderr)
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		if !strings.HasPrefix(line, "  ollama ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			return fields[1]
		}
	}
	t.Fatalf("no ollama models listed:\n%s", res.Stdout)
	return ""
}

var (
	fixedRe   = regexp.MustCompile(`return\s+a\s*\+\s*b`)
	mulIntact = regexp.MustCompile(`return\s+a\s*\*\s*b`)
)

// TestLive_FixBug drives a real Ollama model against a scratch project
// with the same deliberate bug as scripts/e2e.ts: add() subtracts instead
// of adding. It asserts the file is actually fixed on disk, mul() is left
// alone, and at least one tool call happened — the same three checks
// scripts/e2e.ts makes, ported.
func TestLive_FixBug(t *testing.T) {
	if os.Getenv("HARNESS_E2E_LIVE") != "1" {
		t.Skip("set HARNESS_E2E_LIVE=1 to run (talks to a real model over the network)")
	}

	home, sessDir := scratchHome(t)
	proj := scratchProject(t) // src/math.js: add() subtracts, mul() is correct

	model := os.Getenv("HARNESS_LIVE_MODEL")
	if model == "" {
		model = "ollama/" + firstToolCapableOllamaModel(t, home)
	}
	t.Logf("live model: %s", model)

	env := map[string]string{
		"HOME":                  home,
		"HARNESS_SESSIONS_DIR":  sessDir,
		"OLLAMA_HOST":           os.Getenv("OLLAMA_HOST"),
		"OLLAMA_CONTEXT_LENGTH": os.Getenv("OLLAMA_CONTEXT_LENGTH"),
	}

	prompt := "In src/math.js the add function is wrong - it subtracts instead of adding. " +
		"Read the file, fix only that bug, and leave everything else alone."

	res := runBinary(t, harnessBin, proj, env, 5*time.Minute,
		"-p", prompt,
		"--model", model,
		"--output-format", "json",
		"--allowed-tools", "Read,Edit,Write,Bash",
		"--permission-mode", "acceptEdits",
	)
	t.Logf("stdout:\n%s\nstderr:\n%s", res.Stdout, res.Stderr)
	if res.Code != 0 {
		t.Fatalf("harness -p: exit code %d", res.Code)
	}

	after, err := os.ReadFile(proj + "/src/math.js")
	if err != nil {
		t.Fatal(err)
	}
	fixed := fixedRe.MatchString(string(after))
	mulOK := mulIntact.MatchString(string(after))
	t.Logf("add() fixed: %v, mul() intact: %v\nresulting file:\n%s", fixed, mulOK, after)
	if !fixed {
		t.Error("add() was not fixed")
	}
	if !mulOK {
		t.Error("mul() was damaged")
	}
	var parsed struct {
		ToolCalls []map[string]any `json:"toolCalls"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &parsed); err != nil {
		t.Fatalf("parse json output: %v\n%s", err, res.Stdout)
	}
	if len(parsed.ToolCalls) == 0 {
		t.Error("no tool calls happened")
	}
}
