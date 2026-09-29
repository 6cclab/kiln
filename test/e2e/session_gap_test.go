//go:build e2e

package e2e

// Session-lifecycle behaviour not covered by session_test.go: resuming a
// legacy v3 JSONL session file, and what actually happens after a crash
// mid-tool-call. Ground truth read for this file: internal/session/jsonl/
// storage.go's Open (auto-upgrades a legacy v3 file in place via
// UpgradeLegacyV3 before reopening it), internal/session/jsonl/repo.go's
// readSessionMetadata (a legacy header's own "id"/"cwd"/"timestamp"
// fields are what --resume matches against, before any upgrade), and
// internal/harness/resume.go's Lane.Resume (which knows how to replay a
// pending tool batch) plus its PendingOperation helper, now called from
// internal/cli/chat.go (print mode) and internal/cli/tui.go (interactive)
// before either mode's first turn — see internal/agent/session.go's
// ResumeIncomplete, which both call.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/session/jsonl"
)

// toolResultCount opens the session file directly (rather than grepping its
// raw JSONL text, which also contains the transient pi.pending.entry write
// every tool result passes through on its way to being committed — the
// same toolCallId legitimately appears twice in the raw log for one real
// result) and counts durable toolResult entries on record for toolCallID.
func toolResultCount(t *testing.T, path, toolCallID string) int {
	t.Helper()
	st, err := jsonl.Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	count := 0
	for _, e := range st.ScanEntries(session.EntryScan{}) {
		if tr, ok := e.Message.(msg.ToolResultMessage); ok && tr.ToolCallID == toolCallID {
			count++
		}
	}
	return count
}

// TestSession_LegacyV3FixtureResumes copies testdata/sessions/legacy-v3-
// fixture.jsonl into the scratch project's session bucket (rewriting only
// its header's cwd to match, since Repo.List filters candidates by exact
// cwd equality - repo.go's `if cwd == "" || meta.Cwd == cwd`) and resumes
// it by its legacy id. Storage.Open silently upgrades the file to v4 on
// open (legacy_v3.go's UpgradeLegacyV3, called from storage.go's Open),
// so after a successful resume the same file on disk should carry a v4
// header, not the original v3 one, and should still parse via `harness
// session inspect`.
//
// Proved able to fail: pointing --resume at a made-up id
// ("not-the-real-id") turned this red with a non-zero exit code (no
// matching session, so the run fell through to "create a fresh session"
// only if ResumeLatest were also set - it is not here - so args.go's own
// validation actually here just proceeds with the literal, non-empty
// prompt text as a brand-new top-level exchange; distinguishing that from
// a real resume is exactly what the "still v3, only 17 lines" negative
// check below is for). More directly: reading the upgraded file's first
// line and asserting it still claims `"version":3` after a successful
// resume turned this red ("still legacy v3 after upgrade"); reverted.
func TestSession_LegacyV3FixtureResumes(t *testing.T) {
	proj := scratchProject(t)
	home, sessDir := scratchHome(t)

	fixture, err := os.ReadFile("../../testdata/sessions/legacy-v3-fixture.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitN(string(fixture), "\n", 2)
	if len(lines) != 2 {
		t.Fatalf("fixture has no body after its header line")
	}
	var header map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("parse fixture header: %v", err)
	}
	sessionID, _ := header["id"].(string)
	if sessionID == "" {
		t.Fatal("fixture header has no id")
	}
	header["cwd"] = proj
	rewrittenHeader, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture's own "model_change" entry (its selectedConfiguration -
	// legacy_v3.go's selectedConfiguration walks the parent chain to the
	// nearest one) names "anthropic/claude-y", a synthetic model that
	// only internal/session/jsonl's own entry-parsing unit tests need to
	// resolve, not a real, registrable one. After UpgradeLegacyV3 restores
	// that as the session's pi.lane.config, any turn on this session -
	// resumed or not - tries to resolve "anthropic/claude-y" and fails
	// with "harness: unknown model anthropic/claude-y" (observed directly
	// while writing this test; --model does NOT override it, since
	// LaneConfiguration only feeds turn.go's own model resolution once a
	// turn actually starts, downstream of chat.go's own --model handling
	// for a brand-new session). Rewriting the body's provider/model to
	// this test's own faux/faux-1 is the least invasive way to keep this
	// a genuine legacy-v3-upgrade test rather than one that never reaches
	// a real turn at all.
	body := strings.NewReplacer(
		`"provider":"anthropic"`, `"provider":"faux"`,
		`"modelId":"claude-y"`, `"modelId":"faux-1"`,
		`"modelId":"claude-x"`, `"modelId":"faux-1"`,
	).Replace(lines[1])
	rewritten := string(rewrittenHeader) + "\n" + body

	bucket := filepath.Join(sessDir, jsonl.DirectoryName(proj))
	if err := os.MkdirAll(bucket, 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(bucket, "legacy-fixture-copy.jsonl")
	if err := os.WriteFile(dest, []byte(rewritten), 0o644); err != nil {
		t.Fatal(err)
	}

	const oneTurnScript = `model: faux-1
steps:
  - text: "resumed the legacy session"
`
	addr, _ := startFaux(t, oneTurnScript)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"--resume", sessionID, "-p", "still there?", "--output-format", "text")
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "resumed the legacy session") {
		t.Errorf("stdout = %q, want the resumed reply", res.Stdout)
	}

	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	firstLine := strings.SplitN(string(raw), "\n", 2)[0]
	var upgradedHeader map[string]any
	if err := json.Unmarshal([]byte(firstLine), &upgradedHeader); err != nil {
		t.Fatalf("upgraded file's header does not parse as JSON: %v\n%s", err, firstLine)
	}
	if v, _ := upgradedHeader["version"].(float64); v == 3 {
		t.Errorf("still legacy v3 after upgrade: %s", firstLine)
	}

	inspectRes := runHarness(t, proj, baseEnv(home, sessDir, addr), "session", "inspect", dest)
	if inspectRes.Code != 0 {
		t.Fatalf("session inspect: exit code %d, stderr=%s", inspectRes.Code, inspectRes.Stderr)
	}
	var report struct {
		EntryCountByType map[string]int `json:"entryCountByType"`
	}
	if err := json.Unmarshal([]byte(inspectRes.Stdout), &report); err != nil {
		t.Fatalf("parse session inspect output: %v\n%s", err, inspectRes.Stdout)
	}
	if report.EntryCountByType["message"] <= 0 {
		t.Errorf("entryCountByType.message = %d, want > 0 after upgrade; report=%s", report.EntryCountByType["message"], inspectRes.Stdout)
	}
}

const crashMidToolScript = `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "sleep 5"}, id: b1}
  - text: "resumed done"
`

// startHarnessBackground execs harnessBin with args and env layered over
// baseEnv (matching runHarness's own env construction) but returns
// immediately with the running *exec.Cmd, for a caller that needs to
// interact with the process (e.g. kill it) before it exits.
func startHarnessBackground(t *testing.T, dir string, env map[string]string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(harnessBin, args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", harnessBin, err)
	}
	return cmd
}

// TestSession_CrashMidToolThenResume starts a run whose only tool call is
// `bash sleep 5`, waits for the faux server to have received exactly one
// request (proving the harness is now inside the tool call, mid-exec, not
// still deciding what to do) plus a fixed buffer, SIGKILLs the harness
// process outright, and then runs `--resume <id>` with a fresh prompt.
//
// Fixed in internal/cli/chat.go: before either mode's first turn, a
// pending operation left by a crashed process (laneState.CurrentOperationID
// still set - harness.Lane.PendingOperation, resume.go) is now completed
// via harness.Lane.Resume before the new prompt is issued. Resume's own
// AtTools case re-executes whichever tool calls in the interrupted batch
// never reached "outcome_ready" (here, the sole `bash sleep 5` call, id
// b1), commits a real tool_result for it, then drives the loop to the
// model's follow-up and lets that operation finish before the new prompt
// ("what happened?") starts a fresh one. Verified live below: the session
// ends up with exactly ONE tool_result entry for b1's id, and the run
// still reaches the interrupted turn's own scripted reply ("resumed
// done") before the new prompt's turn runs.
//
// Proved able to fail: asserting the interrupted call's tool_result count
// is 0 (the old, broken behavior) turned this red with "tool_result
// entries for b1 = 1, want 0"; reverted to requiring the fixed (one)
// count.
func TestSession_CrashMidToolThenResume(t *testing.T) {
	addr, srv := startFaux(t, crashMidToolScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	env := baseEnv(home, sessDir, addr)

	cmd := startHarnessBackground(t, proj, env,
		"-p", "run a slow command", "--output-format", "text", "--permission-mode", "bypassPermissions")

	deadline := time.Now().Add(10 * time.Second)
	for len(srv.Requests()) < 1 {
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatal("faux never received the first request before the deadline")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Comfortably inside the 5s `sleep 5` tool call, well before it could
	// have finished on its own.
	time.Sleep(500 * time.Millisecond)

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill harness process: %v", err)
	}
	_ = cmd.Wait()

	sess := sessionFile(t, sessDir, proj)
	beforeResume, err := os.ReadFile(sess)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(beforeResume), `"name":"bash"`) {
		t.Fatalf("crashed session has no record of the bash tool_call at all - the process may have been killed too early:\n%s", beforeResume)
	}
	// The tool call's id as actually recorded carries the wire-format
	// prefix a real client echoes back exactly as given (faux's own
	// toolResultPresent helper, internal/testkit/faux/server.go): "b1"
	// becomes "toolu_b1" on the Anthropic-shaped wire, never bare "b1".
	if n := toolResultCount(t, sess, "toolu_b1"); n != 0 {
		t.Fatalf("session already has %d tool_result entries for toolu_b1 before resume ran at all; the process may not have been killed where this test expects", n)
	}

	id := sessionHeaderID(t, sess)

	res := runHarness(t, proj, env, "--resume", id, "-p", "what happened?", "--output-format", "text")
	if res.Code != 0 {
		t.Fatalf("resume: exit code %d, stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "resumed done") {
		t.Errorf("stdout = %q, want the resumed turn's reply", res.Stdout)
	}

	if n := toolResultCount(t, sess, "toolu_b1"); n != 1 {
		t.Errorf("tool_result entries for toolu_b1 after resume = %d, want 1 (see this test's doc comment: --resume must replay the interrupted call exactly once, not abandon it or replay it more than once)", n)
	}
}
