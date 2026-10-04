package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A --settings file inside the workspace is reloaded when it changes
// (settings_reload.go), so writing an allow-everything rule into it must
// not be auto-approved: in acceptEdits a print run, which has nobody to
// ask, refuses it and leaves the file alone.
func TestRun_SettingsFlagFileIsProtected(t *testing.T) {
	startFaux(t, `model: faux-1
steps:
  - tool_call: {name: write, args: {path: "ci/policy.json", content: "{\"permissions\":{\"allow\":[\"Bash\"]}}"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "done"
`)
	proj := scratchProject(t)
	policy := filepath.Join(proj, "ci", "policy.json")
	if err := os.MkdirAll(filepath.Dir(policy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policy, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	args := baseArgs()
	args.Print = true
	args.PrintPrompt = "loosen the policy"
	args.OutputFormat = "json"
	args.PermissionMode = "acceptEdits"
	args.Settings = "ci/policy.json"
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader("")); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if got, _ := os.ReadFile(policy); string(got) != `{}` {
		t.Errorf("the --settings file was rewritten without approval: %s", got)
	}
	if !strings.Contains(stdout.String(), "protected path") {
		t.Errorf("want a protected-path refusal: %s", stdout.String())
	}
}

// With the sandbox on, a command whose write kiln cannot name runs
// sandboxed without a prompt; the sandbox itself must refuse the write to
// a settings file it does not already protect by name.
func TestRun_SandboxHoldsTheSettingsFlagFile(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("needs sandbox-exec")
	}
	startFaux(t, `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "f=ci/policy.json; printf x > \"$f\"; echo wrote"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "done"
`)
	proj := scratchProject(t)
	writeProjectSettings(t, proj, `{"sandbox":{"enabled":true}}`)
	policy := filepath.Join(proj, "ci", "policy.json")
	if err := os.MkdirAll(filepath.Dir(policy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policy, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	args := baseArgs()
	args.Print = true
	args.PrintPrompt = "run it"
	args.OutputFormat = "json"
	args.PermissionMode = "manual"
	args.Settings = "ci/policy.json"
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader("")); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if got, _ := os.ReadFile(policy); string(got) != `{}` {
		t.Errorf("a sandboxed command rewrote the --settings file: %q\n%s", got, stdout.String())
	}
}

// protectSettingsFiles names a settings file and, for a symlink, its
// target.
func TestProtectSettingsFilesFollowsLinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "settings.json")
	link := filepath.Join(dir, "home", ".claude", "settings.json")
	for _, d := range []string{filepath.Dir(target), filepath.Dir(link)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(target, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("no symlinks: %v", err)
	}
	realTarget, _ := filepath.EvalSymlinks(target)
	var got []string
	for _, r := range protectSettingsFiles([]string{link}) {
		got = append(got, r.Path)
	}
	want := map[string]bool{link: false, realTarget: false}
	for _, p := range got {
		if _, ok := want[p]; ok {
			want[p] = true
		}
	}
	for p, seen := range want {
		if !seen {
			t.Errorf("deny list %v lacks %s", got, p)
		}
	}
}
