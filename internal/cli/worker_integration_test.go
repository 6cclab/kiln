package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunWorkerBatchAutomaticIntegration(t *testing.T) {
	for _, tc := range []struct {
		name       string
		userOptIn  bool
		projectOpt bool
		secondFile string
		secondFail bool
		trailing   string
		wantParent bool
		wantApply  bool
		wantReport string
	}{
		{"disjoint", true, false, "two.txt", false, "", false, true, "integrated 2 patches"},
		{"default off", false, false, "two.txt", false, "", false, false, ""},
		{"project cannot opt in", false, true, "two.txt", false, "", false, false, ""},
		{"overlap", true, false, "one.txt", false, "", false, false, "overlap at: one.txt"},
		{"failed worker", true, false, "two.txt", true, "", false, false, "a tool or worker failed"},
		{"later parent write", true, false, "two.txt", false, "write", true, false, "Worker batch was not integrated"},
		{"later tool failure", true, false, "two.txt", false, "read", false, false, "a tool or worker failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HARNESS_FAUX_EXTRA_MODELS", "faux-3")
			parentExtra := ""
			results := "[tc1, tc2]"
			switch tc.trailing {
			case "write":
				parentExtra = "\n        - {name: write, args: {path: \"one.txt\", content: \"parent\\n\"}, id: tc3}"
			case "read":
				parentExtra = "\n        - {name: read, args: {path: \"missing.txt\"}, id: tc3}"
			}
			if parentExtra != "" {
				results = "[tc1, tc2, tc3]"
			}
			second := `  - tool_call: {name: write, args: {path: "` + tc.secondFile + `", content: "second\n"}, id: w2}
  - on_tool_result: w2
    then:
      - text: "second done"
`
			if tc.secondFail {
				second = `  - error: {status: 400, type: invalid_request_error, message: rejected}
`
			}
			script := `models:
  faux-1:
    - tool_calls:
        - {name: task, args: {subagent_type: "general-purpose", description: "first", prompt: "write first", model: "fast"}, id: tc1}
        - {name: task, args: {subagent_type: "general-purpose", description: "second", prompt: "write second", model: "heavy"}, id: tc2}` + parentExtra + `
    - on_tool_results: ` + results + `
      then:
        - text: "parent done"
  faux-2:
    - tool_call: {name: write, args: {path: "one.txt", content: "first\n"}, id: w1}
    - on_tool_result: w1
      then:
        - text: "first done"
  faux-3:
` + second
			srv := startFaux(t, script)
			proj := scratchProject(t)
			git := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", args...)
				cmd.Dir = proj
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
			}
			git("init", "-q")
			git("config", "user.name", "Kiln Test")
			git("config", "user.email", "kiln@example.test")
			if err := os.WriteFile(filepath.Join(proj, "base.txt"), []byte("base\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			git("add", "base.txt")
			git("commit", "-qm", "base")
			settings := `{"orchestration":{"isolatedWorkers":true,"autoIntegrate":true},"modelRoles":{"fast":"faux/faux-2","heavy":"faux/faux-3"}}`
			if tc.userOptIn {
				path := filepath.Join(os.Getenv("HOME"), ".kiln", "settings.json")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				path := filepath.Join(proj, ".claude", "settings.json")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if !tc.projectOpt {
					settings = `{"orchestration":{"isolatedWorkers":true},"modelRoles":{"fast":"faux/faux-2","heavy":"faux/faux-3"}}`
				}
				if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			args := baseArgs()
			args.Print = true
			args.PrintPrompt = "run two workers"
			args.OutputFormat = "text"
			args.PermissionMode = "bypassPermissions"
			var stdout, stderr bytes.Buffer
			if code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader("")); code != 0 {
				t.Fatalf("run exit %d: stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
			for _, name := range []string{"one.txt", "two.txt"} {
				_, err := os.Stat(filepath.Join(proj, name))
				if tc.wantParent && name == "one.txt" {
					data, readErr := os.ReadFile(filepath.Join(proj, name))
					if readErr != nil || string(data) != "parent\n" {
						t.Fatalf("parent write lost or replaced: %q, %v", data, readErr)
					}
					continue
				}
				if tc.wantApply && err != nil {
					t.Fatalf("expected %s applied: %v; stderr=%s", name, err, stderr.String())
				}
				if !tc.wantApply && !os.IsNotExist(err) {
					t.Fatalf("unexpected %s applied: %v", name, err)
				}
			}
			if tc.wantReport != "" {
				if !strings.Contains(stdout.String(), tc.wantReport) {
					t.Fatalf("print output missing durable integration outcome %q: %s", tc.wantReport, stdout.String())
				}
				var durable bool
				if err := filepath.WalkDir(os.Getenv("HARNESS_SESSIONS_DIR"), func(path string, entry os.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if entry.IsDir() || !strings.HasSuffix(path, ".jsonl") {
						return nil
					}
					data, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					if bytes.Contains(data, []byte(`"kilnHook":"WorkerIntegration"`)) && bytes.Contains(data, []byte(tc.wantReport)) {
						durable = true
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if !durable {
					t.Fatalf("session has no durable integration outcome %q", tc.wantReport)
				}
				var found bool
				for _, req := range srv.Requests() {
					if req.Model == "faux-1" && strings.Contains(string(req.Messages), tc.wantReport) {
						found = true
					}
					if req.Model == "faux-1" && strings.Contains(string(req.Messages), "Apply explicitly after review") {
						t.Fatal("automatic batch sent contradictory explicit-apply instruction")
					}
				}
				if !found {
					t.Fatalf("parent request missing %q; stderr=%s", tc.wantReport, stderr.String())
				}
			}
		})
	}
}

func TestRunDispatchHonorsIsolatedShellSetting(t *testing.T) {
	script := `models:
  faux-1:
    - tool_call: {name: task, args: {subagent_type: general-purpose, description: child, prompt: run child, model: fast}, id: task1}
    - on_tool_result: task1
      then:
        - text: parent done
  faux-2:
    - text: child ran
`
	srv := startFaux(t, script)
	scratchProject(t)
	path := filepath.Join(os.Getenv("HOME"), ".kiln", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"orchestration":{"isolatedShell":true},"modelRoles":{"fast":"faux/faux-2"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	args := baseArgs()
	args.Print = true
	args.PrintPrompt = "delegate"
	args.OutputFormat = "text"
	args.PermissionMode = "bypassPermissions"
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader("")); code != 0 {
		t.Fatalf("run exit %d: %s %s", code, stdout.String(), stderr.String())
	}
	for _, request := range srv.Requests() {
		if request.Model == "faux-1" && strings.Contains(string(request.Messages), "isolated shell requires isolated workers") {
			return
		}
	}
	t.Fatalf("parent did not receive configured isolated-shell refusal: stdout=%s stderr=%s", stdout.String(), stderr.String())
}

func TestRunDispatchHonorsProgressLoopSetting(t *testing.T) {
	t.Setenv("HARNESS_FAUX_EXTRA_MODELS", "faux-2")
	script := `models:
  faux-1:
    - tool_call: {name: task, args: {subagent_type: general-purpose, description: child, prompt: inspect, model: fast}, id: task1}
    - on_tool_result: task1
      then:
        - text: parent done
  faux-2:
    - tool_calls:
        - {name: read, args: {path: repeated.txt}, id: read1}
        - {name: read, args: {path: repeated.txt}, id: read2}
        - {name: read, args: {path: repeated.txt}, id: read3}
        - {name: read, args: {path: repeated.txt}, id: read4}
    - on_tool_results: [read1, read2, read3, read4]
      then:
        - text: child done
`
	startFaux(t, script)
	proj := scratchProject(t)
	if err := os.WriteFile(filepath.Join(proj, "repeated.txt"), []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(os.Getenv("HOME"), ".kiln", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"runtimeEfficiency":{"progressLoopLimit":2},"modelRoles":{"fast":"faux/faux-2"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	args := baseArgs()
	args.Print = true
	args.PrintPrompt = "delegate"
	args.OutputFormat = "text"
	args.PermissionMode = "bypassPermissions"
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader("")); code != 0 {
		t.Fatalf("run exit %d: %s %s", code, stdout.String(), stderr.String())
	}
	var notices int
	err := filepath.WalkDir(os.Getenv("HARNESS_SESSIONS_DIR"), func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(path) != ".jsonl" {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		notices += strings.Count(string(data), `"progress-loop"`)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if notices == 0 {
		t.Fatalf("configured loop limit did not reach worker; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
}
