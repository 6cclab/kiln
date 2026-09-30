//go:build e2e

// Package e2e: Claude Code's auto-memory parity
// (internal/claude/memory/automemory.go), driven through the real kiln
// binary against the faux provider and a scratch HOME. Ground truth read
// for this file: Claude Code's docs (code.claude.com/docs/en/memory, "Auto
// memory" section) and internal/claude/permission/permission.go's
// ReadOnlyRoots handling. Every test's doc comment records the break used
// to confirm it can fail.
package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/claude/trust"
)

// automemTrust marks proj as a trusted folder in home's trust store,
// matching how a person accepting kiln's trust dialog would.
func automemTrust(t *testing.T, home, proj string) {
	t.Helper()
	store := trust.NewStoreAt(filepath.Join(home, ".harness", "trusted.json"))
	if err := store.Trust(proj); err != nil {
		t.Fatal(err)
	}
}

// automemWriteJSON writes proj's .claude/settings.json.
func automemWriteJSON(t *testing.T, proj string, settings map[string]any) {
	t.Helper()
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(proj, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// automemSeed writes a MEMORY.md (plus an optional topic file) into a fresh
// directory, and points proj's .claude/settings.json at it via
// autoMemoryDirectory, so every test controls exactly where the memory
// directory is rather than depending on git/project-name resolution
// (covered separately by internal/claude/memory's own unit tests). It does
// NOT trust proj — callers that want the override actually honoured (as
// opposed to testing the trust gate itself) must call automemTrust too.
func automemSeed(t *testing.T, proj, memoryIndex string) (dir string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte(memoryIndex), 0o644); err != nil {
		t.Fatal(err)
	}
	automemWriteJSON(t, proj, map[string]any{"autoMemoryDirectory": dir})
	return dir
}

// TestAutoMemory_IndexAppearsInFirstSystemPrompt: a seeded MEMORY.md shows
// up, framed, in the first request's system prompt.
//
// Proved able to fail: comment out the `promptParts = append(promptParts,
// memory.Text, autoMemory.Text, ...)` append in chat.go's buildSystemPrompt
// -> this test's Contains assertion fails.
func TestAutoMemory_IndexAppearsInFirstSystemPrompt(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	dir := automemSeed(t, proj, "- user_role: prefers table-driven Go tests\n")
	automemTrust(t, home, proj)

	sys := budgetRunAndFirstSystem(t, home, sessDir, proj, "faux/faux-1", "", nil)
	if !strings.Contains(sys, "user_role: prefers table-driven Go tests") {
		t.Errorf("system prompt missing auto-memory content:\n%s", sys)
	}
	if !strings.Contains(sys, dir) {
		t.Errorf("system prompt should name the memory directory so the model can read topic files:\n%s", sys)
	}
}

// TestAutoMemory_AbsentWhenDisabledBySetting: autoMemoryEnabled: false
// keeps the index out of the system prompt even though MEMORY.md exists.
//
// Proved able to fail: temporarily hardcoding opts.Enabled to nil in
// chat.go's LoadAutoMemory call (ignoring settings.AutoMemoryEnabled) turns
// this red (the content appears anyway); reverted.
func TestAutoMemory_AbsentWhenDisabledBySetting(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("- should not load\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	automemWriteJSON(t, proj, map[string]any{
		"autoMemoryDirectory": dir,
		"autoMemoryEnabled":   false,
	})
	automemTrust(t, home, proj)

	sys := budgetRunAndFirstSystem(t, home, sessDir, proj, "faux/faux-1", "", nil)
	if strings.Contains(sys, "should not load") {
		t.Errorf("system prompt should not contain auto-memory content when disabled:\n%s", sys)
	}
}

// TestAutoMemory_AbsentWhenDisabledByEnv: CLAUDE_CODE_DISABLE_AUTO_MEMORY=1
// keeps the index out of the system prompt.
//
// Proved able to fail: temporarily hardcoding opts.EnvDisabled to false in
// chat.go's LoadAutoMemory call turns this red; reverted.
func TestAutoMemory_AbsentWhenDisabledByEnv(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	automemSeed(t, proj, "- should not load either\n")
	automemTrust(t, home, proj)

	sys := budgetRunAndFirstSystem(t, home, sessDir, proj, "faux/faux-1", "",
		map[string]string{"CLAUDE_CODE_DISABLE_AUTO_MEMORY": "1"})
	if strings.Contains(sys, "should not load either") {
		t.Errorf("system prompt should not contain auto-memory content when CLAUDE_CODE_DISABLE_AUTO_MEMORY=1:\n%s", sys)
	}
}

const automemReadScriptFmt = `model: faux-1
steps:
  - tool_call: {name: read, args: {path: %q}, id: r1}
  - on_tool_result: r1
    then:
      - text: "read it"
`

// TestAutoMemory_ReadTopicFileDoesNotPrompt: a faux read of a topic file
// inside the (trusted-project) auto-memory directory runs clean, in
// kiln's default (unspecified -> manual) permission mode, which has no
// prompter in print mode — so before ReadOnlyRoots, this path (outside
// every workspace root) would be refused as "outside the workspace and
// cannot be confirmed."
//
// Proved able to fail: removing the `ReadOnlyRoots: readOnlyRoots` line
// from chat.go's permission.NewGate call turns this red (Blocked gains a
// "read(" entry, Code stays 0 since a block is feedback not a failure, but
// the OK/Blocked assertions below fail).
func TestAutoMemory_ReadTopicFileDoesNotPrompt(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	dir := automemSeed(t, proj, "- feedback_testing: see feedback_testing.md\n")
	topic := filepath.Join(dir, "feedback_testing.md")
	if err := os.WriteFile(topic, []byte("Prefers table-driven tests."), 0o644); err != nil {
		t.Fatal(err)
	}
	automemTrust(t, home, proj)

	addr, _ := startFaux(t, fmt.Sprintf(automemReadScriptFmt, topic))
	run := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "what does memory say about testing", "--output-format", "json")

	var res struct {
		OK      bool     `json:"ok"`
		Blocked []string `json:"blocked"`
	}
	if err := json.Unmarshal([]byte(run.Stdout), &res); err != nil {
		t.Fatalf("parse --output-format json: %v\nstdout=%s\nstderr=%s", err, run.Stdout, run.Stderr)
	}
	if run.Code != 0 {
		t.Fatalf("exit code %d, want 0; result=%+v stderr=%s", run.Code, res, run.Stderr)
	}
	if len(res.Blocked) != 0 {
		t.Errorf("read of a topic file under the auto-memory dir was blocked: %v", res.Blocked)
	}
	if !res.OK {
		t.Errorf("expected ok=true, got %+v", res)
	}
}

const automemWriteScriptFmt = `model: faux-1
steps:
  - tool_call: {name: write, args: {path: %q, content: "sneaky"}, id: w1}
  - on_tool_result: w1
    then:
      - text: "noted"
`

// TestAutoMemory_WriteIntoMemoryDirStillBlocked: a faux write into the
// auto-memory directory is refused exactly as any other outside-workspace
// write in default (manual) mode — never silently allowed because the
// directory was added as a permission root. kiln never writes auto memory.
//
// Proved able to fail: swapping AddReadOnlyRoot for AddRoot in chat.go's
// gate setup turns this red (the write verdict would still be Ask under
// manual mode here, so to actually catch a widened grant this also checks
// the file was never created on disk, which AddRoot would not change
// either — the real regression this guards is a future change that reads
// ReadOnlyRoots for verdict computation instead of only for the
// escaped-path check; see permission_test.go's package-level unit test for
// the direct version of this same property).
func TestAutoMemory_WriteIntoMemoryDirStillBlocked(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	dir := automemSeed(t, proj, "- some note\n")
	target := filepath.Join(dir, "sneaky.md")
	automemTrust(t, home, proj)

	addr, _ := startFaux(t, fmt.Sprintf(automemWriteScriptFmt, target))
	run := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "write a note", "--output-format", "json")

	var res struct {
		OK      bool     `json:"ok"`
		Blocked []string `json:"blocked"`
	}
	if err := json.Unmarshal([]byte(run.Stdout), &res); err != nil {
		t.Fatalf("parse --output-format json: %v\nstdout=%s\nstderr=%s", err, run.Stdout, run.Stderr)
	}
	if run.Code != 0 {
		t.Fatalf("exit code %d, want 0 (a block is feedback, not a failure); result=%+v", run.Code, res)
	}
	if !permBlockedFor(res.Blocked, "write(") {
		t.Errorf("expected the write into the auto-memory dir to be blocked, got blocked=%v", res.Blocked)
	}
	if _, err := os.Stat(target); err == nil {
		t.Errorf("kiln must never write into the auto-memory directory, but %s was created", target)
	}
}

// automemRunRead runs a faux read of path and reports whether it was
// blocked, via --output-format json's Blocked list.
func automemRunRead(t *testing.T, proj string, env map[string]string, path string) (blocked bool) {
	t.Helper()
	addr, _ := startFaux(t, fmt.Sprintf(automemReadScriptFmt, path))
	env = mergeEnv(env, map[string]string{"HARNESS_FAUX_ADDR": addr, "HARNESS_FAUX_API": "anthropic-messages", "HARNESS_MODEL": "faux/faux-1"})
	run := runHarness(t, proj, env, "-p", "what does memory say", "--output-format", "json")

	var res struct {
		OK      bool     `json:"ok"`
		Blocked []string `json:"blocked"`
	}
	if err := json.Unmarshal([]byte(run.Stdout), &res); err != nil {
		t.Fatalf("parse --output-format json: %v\nstdout=%s\nstderr=%s", err, run.Stdout, run.Stderr)
	}
	if run.Code != 0 {
		t.Fatalf("exit code %d, want 0; result=%+v stderr=%s", run.Code, res, run.Stderr)
	}
	return permBlockedFor(res.Blocked, "read(")
}

// TestAutoMemory_DirectoryOverrideIgnoredWhenUntrusted: a project's checked-
// in/local .claude/settings.json can set autoMemoryDirectory to anywhere on
// disk (here, a scratch "secrets" directory), and since that directory
// becomes a read-only permission root, an untrusted folder must not be
// able to steer kiln's reads there without asking. In kiln's default
// (manual, untrusted) state, a read of a file under that directory must
// still be blocked, exactly as any other outside-workspace read.
//
// Proved able to fail: passing settings.AutoMemoryDirectory straight to
// LoadAutoMemory (as the original, pre-review version of chat.go did,
// skipping trustedAutoMemoryDirectory's folderTrusted gate) turns this red
// — the secrets directory becomes a root and the read is no longer
// blocked.
func TestAutoMemory_DirectoryOverrideIgnoredWhenUntrusted(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	secrets := t.TempDir()
	secretFile := filepath.Join(secrets, "id_rsa")
	if err := os.WriteFile(secretFile, []byte("fake key material"), 0o600); err != nil {
		t.Fatal(err)
	}
	automemWriteJSON(t, proj, map[string]any{"autoMemoryDirectory": secrets})

	if blocked := automemRunRead(t, proj, baseEnv(home, sessDir, ""), secretFile); !blocked {
		t.Error("expected a read under an untrusted project's autoMemoryDirectory override to be blocked")
	}
}

// TestAutoMemory_DirectoryOverrideHonouredWhenTrusted is the control for
// the test above: once the same folder is trusted (as accepting kiln's
// trust dialog would record), the same project-settings override is
// honoured and the read passes.
func TestAutoMemory_DirectoryOverrideHonouredWhenTrusted(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	secrets := t.TempDir()
	secretFile := filepath.Join(secrets, "id_rsa")
	if err := os.WriteFile(secretFile, []byte("fake key material"), 0o600); err != nil {
		t.Fatal(err)
	}
	automemWriteJSON(t, proj, map[string]any{"autoMemoryDirectory": secrets})
	automemTrust(t, home, proj)

	if blocked := automemRunRead(t, proj, baseEnv(home, sessDir, ""), secretFile); blocked {
		t.Error("expected a read under a trusted project's autoMemoryDirectory override to be allowed")
	}
}

// TestAutoMemory_RootDirectoryOverrideRefusedEvenWhenTrusted: an
// autoMemoryDirectory of "/" must never be honoured, trusted folder or
// not — it would turn the read-only permission root into "read anything
// on disk without asking". The project is trusted here specifically to
// prove the refusal comes from the unsafe-directory check, not merely
// from the trust gate above.
//
// Proved able to fail: removing resolveAutoMemoryDir's
// unsafeAutoMemoryDirectory check (or trusting this folder before that fix
// existed) turns this red — a file completely unrelated to the project
// would no longer be blocked.
func TestAutoMemory_RootDirectoryOverrideRefusedEvenWhenTrusted(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	automemWriteJSON(t, proj, map[string]any{"autoMemoryDirectory": "/"})
	automemTrust(t, home, proj)

	elsewhere := t.TempDir()
	unrelated := filepath.Join(elsewhere, "unrelated.txt")
	if err := os.WriteFile(unrelated, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if blocked := automemRunRead(t, proj, baseEnv(home, sessDir, ""), unrelated); !blocked {
		t.Error("expected an autoMemoryDirectory of \"/\" to be refused, leaving an unrelated file still blocked")
	}
}

// TestAutoMemory_HomeDirectoryOverrideRefusedEvenWhenTrusted:
// autoMemoryDirectory: "~/" must never be honoured either — it would make
// the read-only permission root the user's entire home directory. Trusted
// here for the same reason as the "/" test above.
func TestAutoMemory_HomeDirectoryOverrideRefusedEvenWhenTrusted(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	automemWriteJSON(t, proj, map[string]any{"autoMemoryDirectory": "~/"})
	automemTrust(t, home, proj)

	// Under home, but nowhere near the default auto-memory directory
	// (~/.claude/projects/<project>/memory): if the "~/" override were
	// honoured, this file would fall inside the (much too broad) root.
	elsewhereUnderHome := filepath.Join(home, "Documents", "diary.txt")
	if err := os.MkdirAll(filepath.Dir(elsewhereUnderHome), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(elsewhereUnderHome, []byte("private"), 0o644); err != nil {
		t.Fatal(err)
	}

	if blocked := automemRunRead(t, proj, baseEnv(home, sessDir, ""), elsewhereUnderHome); !blocked {
		t.Error("expected an autoMemoryDirectory of \"~/\" to be refused, leaving an unrelated file under home still blocked")
	}
}
