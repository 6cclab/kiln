//go:build e2e

package e2e

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// sessionHeaderID reads path's first line (the JSONL header) and returns
// its "id" field.
func sessionHeaderID(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		t.Fatalf("%s: no lines", path)
	}
	var header struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(sc.Bytes(), &header); err != nil {
		t.Fatalf("parse header line of %s: %v", path, err)
	}
	if header.ID == "" {
		t.Fatalf("%s: header has no id: %s", path, sc.Text())
	}
	return header.ID
}

// entryCount counts newline-terminated lines in path, minus the one header
// line, as a coarse "did this grow" signal independent of entry type.
func lineCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	return len(lines)
}

func sha256File(t *testing.T, path string) [32]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(data)
}

// TestSession_ContinueLatest_ResumesSameFile runs the harness binary twice
// with -c: the first run creates a session, the second (-c) must resume it
// in place — same file, growing entry count — rather than creating a new
// one.
func TestSession_ContinueLatest_ResumesSameFile(t *testing.T) {
	addr, _ := startFaux(t, unreadScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	env := baseEnv(home, sessDir, addr)

	res1 := runHarness(t, proj, env, "-p", "hello", "--output-format", "text")
	if res1.Code != 0 {
		t.Fatalf("first run: exit code %d, stderr=%s", res1.Code, res1.Stderr)
	}
	sess1 := sessionFile(t, sessDir, proj)
	lines1 := lineCount(t, sess1)

	res2 := runHarness(t, proj, env, "-c", "-p", "hello again", "--output-format", "text")
	if res2.Code != 0 {
		t.Fatalf("second run: exit code %d, stderr=%s", res2.Code, res2.Stderr)
	}
	sess2 := sessionFile(t, sessDir, proj)
	if sess2 != sess1 {
		t.Fatalf("-c created a new session file: first=%s second=%s", sess1, sess2)
	}
	lines2 := lineCount(t, sess2)
	if lines2 <= lines1 {
		t.Errorf("entry count did not grow: first run %d lines, second run %d lines", lines1, lines2)
	}
}

// TestSession_ResumeByIDPrefix runs once to create a session, then a
// second, independent run with --resume <8-char prefix of the first run's
// session id> and checks it reopened the same file (rather than creating a
// new one).
func TestSession_ResumeByIDPrefix(t *testing.T) {
	addr, _ := startFaux(t, unreadScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	env := baseEnv(home, sessDir, addr)

	res1 := runHarness(t, proj, env, "-p", "hello", "--output-format", "text")
	if res1.Code != 0 {
		t.Fatalf("first run: exit code %d, stderr=%s", res1.Code, res1.Stderr)
	}
	sess1 := sessionFile(t, sessDir, proj)
	id := sessionHeaderID(t, sess1)
	prefix := id[:8]

	res2 := runHarness(t, proj, env, "--resume", prefix, "-p", "hello again", "--output-format", "text")
	if res2.Code != 0 {
		t.Fatalf("second run: exit code %d, stderr=%s", res2.Code, res2.Stderr)
	}
	sess2 := sessionFile(t, sessDir, proj)
	if sess2 != sess1 {
		t.Fatalf("--resume %s created a new session file instead of reopening %s: got %s", prefix, sess1, sess2)
	}
}

// TestSession_ForkSession_LeavesSourceUnchanged runs once, then
// --fork-session --resume <id>: the fork produces a second file, and the
// source file's bytes are unchanged (sha256 comparison), matching
// internal/agent's own TestStartForkSessionLeavesSourceUnchanged.
func TestSession_ForkSession_LeavesSourceUnchanged(t *testing.T) {
	addr, _ := startFaux(t, unreadScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	env := baseEnv(home, sessDir, addr)

	res1 := runHarness(t, proj, env, "-p", "hello", "--output-format", "text")
	if res1.Code != 0 {
		t.Fatalf("first run: exit code %d, stderr=%s", res1.Code, res1.Stderr)
	}
	sess1 := sessionFile(t, sessDir, proj)
	id := sessionHeaderID(t, sess1)
	beforeSum := sha256File(t, sess1)

	res2 := runHarness(t, proj, env, "--fork-session", "--resume", id, "-p", "forked", "--output-format", "text")
	if res2.Code != 0 {
		t.Fatalf("fork run: exit code %d, stderr=%s", res2.Code, res2.Stderr)
	}

	files := sessionFiles(t, sessDir, proj)
	if len(files) != 2 {
		t.Fatalf("got %d session files after forking, want 2: %v", len(files), files)
	}

	afterSum := sha256File(t, sess1)
	if beforeSum != afterSum {
		t.Fatal("forking modified the source file")
	}

	var forked string
	for _, f := range files {
		if f != sess1 {
			forked = f
		}
	}
	if forked == "" {
		t.Fatal("could not find the forked session file")
	}
	if forked == sess1 {
		t.Fatal("fork produced the same file as its source")
	}
}

// TestSession_Inspect runs `harness session inspect <file>` on a session
// produced by a normal print-mode run and checks the output parses as JSON
// with at least one "message" entry.
func TestSession_Inspect(t *testing.T) {
	addr, _ := startFaux(t, unreadScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	env := baseEnv(home, sessDir, addr)

	res1 := runHarness(t, proj, env, "-p", "hello", "--output-format", "text")
	if res1.Code != 0 {
		t.Fatalf("run: exit code %d, stderr=%s", res1.Code, res1.Stderr)
	}
	sess := sessionFile(t, sessDir, proj)

	res2 := runHarness(t, proj, env, "session", "inspect", sess)
	if res2.Code != 0 {
		t.Fatalf("session inspect: exit code %d, stderr=%s", res2.Code, res2.Stderr)
	}

	var report struct {
		EntryCountByType map[string]int `json:"entryCountByType"`
	}
	if err := json.Unmarshal([]byte(res2.Stdout), &report); err != nil {
		t.Fatalf("parse session inspect output: %v\n%s", err, res2.Stdout)
	}
	if report.EntryCountByType["message"] <= 0 {
		t.Errorf("entryCountByType.message = %d, want > 0; report = %s", report.EntryCountByType["message"], res2.Stdout)
	}
}
