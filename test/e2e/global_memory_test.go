//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlobalPreferenceCLIAndChatRetrieval(t *testing.T) {
	home, sessDir := scratchHome(t)
	projectA, projectB := scratchProject(t), scratchProject(t)
	addr, server := startFaux(t, "model: faux-1\nsteps:\n  - text: done\n")
	env := baseEnv(home, sessDir, addr)
	run := func(project string, args ...string) {
		t.Helper()
		result := runHarness(t, project, env, args...)
		if result.Code != 0 {
			t.Fatalf("kiln %v: exit=%d stderr=%s", args, result.Code, result.Stderr)
		}
	}
	run(projectA, "memory", "global", "remember", "runner", "Use go test")
	run(projectB, "-p", "runner tests", "--permission-mode", "bypassPermissions")
	requests := server.Requests()
	if len(requests) != 1 || !strings.Contains(string(requests[0].Messages), "Use go test") || !strings.Contains(string(requests[0].Messages), "user:preferences") {
		t.Fatalf("global preference absent from other project: %+v", requests)
	}
	server.Reset()
	run(projectA, "memory", "remember", "runner", "Use make check")
	run(projectA, "-p", "runner tests", "--permission-mode", "bypassPermissions")
	requests = server.Requests()
	if len(requests) != 1 || !strings.Contains(string(requests[0].Messages), "Use make check") || strings.Contains(string(requests[0].Messages), "Use go test") {
		t.Fatalf("project preference did not win: %+v", requests)
	}
	server.Reset()
	run(projectA, "memory", "global", "forget", "runner")
	run(projectB, "-p", "runner tests", "--permission-mode", "bypassPermissions")
	requests = server.Requests()
	if len(requests) != 1 || strings.Contains(string(requests[0].Messages), "Use go test") {
		t.Fatalf("forgotten preference leaked: %+v", requests)
	}
	server.Reset()
	run(projectA, "memory", "global", "remember", "runner", "Use go test")
	settings := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(settings, []byte(`{"personalMemoryEnabled":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	run(projectB, "-p", "runner tests", "--permission-mode", "bypassPermissions", "--settings", settings)
	requests = server.Requests()
	if len(requests) != 1 || strings.Contains(string(requests[0].Messages), "Use go test") {
		t.Fatalf("disabled personal memory leaked global preference: %+v", requests)
	}
}
