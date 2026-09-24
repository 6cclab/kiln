//go:build e2e

// interop_test.go checks that a session the Go harness writes is a session
// the TypeScript reference's own pi-agent-core can open and read back the
// same entry count from — the actual cross-implementation compatibility
// claim, not just "the file looks like JSON". Gated behind
// HARNESS_NODE_INTEROP=1 because it requires a real Node checkout of
// /Users/andrepato/projects/harness with node_modules installed (read-only;
// this package never writes into that tree).
package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// nodeInteropScript imports the TS reference's session repo and execution
// env, opens the session at (sessionsRoot=sessDir, cwd=proj, id=id), and
// prints "ENTRIES <n>" where n is entries.length from findEntries. It is
// passed as -e source with --experimental-strip-types so no build step is
// needed against the TS sources' own .ts imports.
const nodeInteropScript = `
import { JsonlSessionRepo } from "@earendil-works/pi-agent-core";
import { NodeExecutionEnv } from "@earendil-works/pi-agent-core/node";
import { BACKGROUND_CONTEXT } from "@earendil-works/pi-agent-core";

const sessDir = process.env.HARNESS_INTEROP_SESSDIR;
const proj = process.env.HARNESS_INTEROP_PROJ;
const id = process.env.HARNESS_INTEROP_ID;

const env = new NodeExecutionEnv({ cwd: proj });
const repo = new JsonlSessionRepo({ fileSystem: env, sessionsRoot: sessDir });

const candidates = await repo.list({ cwd: proj }, BACKGROUND_CONTEXT);
const match = candidates.find((m) => m.id === id);
if (!match) {
	console.error("no candidate with id " + id + " among " + candidates.map((c) => c.id).join(","));
	process.exit(1);
}

const session = await repo.open(match, BACKGROUND_CONTEXT);
const entries = await session.findEntries(undefined, BACKGROUND_CONTEXT);
console.log("ENTRIES " + entries.length);
`

const harnessNodeRepo = "/Users/andrepato/projects/harness"

// TestInterop_NodeReadsGoSession runs a normal print-mode run through the
// Go binary, then execs Node against @earendil-works/pi-agent-core (the
// same package the TypeScript harness depends on) to open that exact
// session file and count its entries, and checks the count matches what
// `harness session inspect` reports for the same file.
func TestInterop_NodeReadsGoSession(t *testing.T) {
	if os.Getenv("HARNESS_NODE_INTEROP") != "1" {
		t.Skip("set HARNESS_NODE_INTEROP=1 to run (requires a Node checkout with node_modules at " + harnessNodeRepo + ")")
	}
	if _, err := os.Stat(harnessNodeRepo); err != nil {
		t.Fatalf("Node interop checkout not found at %s: %v", harnessNodeRepo, err)
	}

	addr, _ := startFaux(t, fixBugScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	env := baseEnv(home, sessDir, addr)

	res := runHarness(t, proj, env,
		"-p", "fix the bug",
		"--output-format", "text",
		"--permission-mode", "acceptEdits",
		"--allowed-tools", "Read",
	)
	if res.Code != 0 {
		t.Fatalf("harness run: exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	sess := sessionFile(t, sessDir, proj)
	id := sessionHeaderID(t, sess)

	inspect := runHarness(t, proj, env, "session", "inspect", sess)
	if inspect.Code != 0 {
		t.Fatalf("session inspect: exit code %d, stderr=%s", inspect.Code, inspect.Stderr)
	}
	var report struct {
		EntryCountByType map[string]int `json:"entryCountByType"`
	}
	if err := json.Unmarshal([]byte(inspect.Stdout), &report); err != nil {
		t.Fatalf("parse session inspect output: %v\n%s", err, inspect.Stdout)
	}
	goEntries := 0
	for _, n := range report.EntryCountByType {
		goEntries += n
	}

	cmd := exec.Command("node", "--experimental-strip-types", "-e", nodeInteropScript)
	cmd.Dir = harnessNodeRepo
	cmd.Env = append(os.Environ(),
		"HARNESS_INTEROP_SESSDIR="+sessDir,
		"HARNESS_INTEROP_PROJ="+proj,
		"HARNESS_INTEROP_ID="+id,
	)
	out, err := cmd.CombinedOutput()
	t.Logf("node interop output:\n%s", out)
	if err != nil {
		t.Fatalf("node interop script failed: %v\n%s", err, out)
	}

	var nodeEntries int
	var found bool
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(line, "ENTRIES ") {
			n, err := strconv.Atoi(strings.TrimPrefix(line, "ENTRIES "))
			if err != nil {
				t.Fatalf("parse ENTRIES line %q: %v", line, err)
			}
			nodeEntries = n
			found = true
		}
	}
	if !found {
		t.Fatalf("node interop script printed no ENTRIES line:\n%s", out)
	}
	if nodeEntries != goEntries {
		t.Errorf("Node read %d entries, Go's session inspect reports %d (sum of entryCountByType %v)", nodeEntries, goEntries, report.EntryCountByType)
	}
}
