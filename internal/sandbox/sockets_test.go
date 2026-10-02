package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Linux sandbox hides the session bus, docker and the ssh agent's
// socket unless the settings open Unix sockets.
func TestHiddenSockets(t *testing.T) {
	env := map[string]string{"SSH_AUTH_SOCK": "/tmp/ssh-x/agent.1", "DOCKER_HOST": "unix:///home/u/.docker/run/docker.sock"}
	get := func(k string) string { return env[k] }
	got := hiddenSockets(Config{}, 1000, get)
	for _, want := range []string{"/run/user/1000/bus", "/var/run/docker.sock", "/run/docker.sock", "/tmp/ssh-x/agent.1", "/home/u/.docker/run/docker.sock"} {
		if !contains(got, want) {
			t.Errorf("%s not hidden: %v", want, got)
		}
	}
	got = hiddenSockets(Config{UnixSockets: []string{"/var/run/docker.sock", "/tmp/ssh-x"}}, 1000, get)
	if contains(got, "/var/run/docker.sock") || contains(got, "/tmp/ssh-x/agent.1") || !contains(got, "/run/user/1000/bus") {
		t.Errorf("allowUnixSockets entries still hidden, or others not: %v", got)
	}
	if got := hiddenSockets(Config{AllowAllUnixSockets: true}, 1000, get); len(got) != 0 {
		t.Errorf("allowAllUnixSockets hides %v", got)
	}

	sock := filepath.Join(t.TempDir(), "agent.sock")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	p := buildPlan(Config{}, ws, []string{ws}, "", t.TempDir(), 0, 0)
	p.HiddenSockets = []string{sock, "/nonexistent/kiln/bus"}
	argv, err := bwrapArgs(p, "/usr/bin/bwrap", "", "/bin/sh", "true", nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, "\x00")
	if !strings.Contains(joined, "--ro-bind\x00/dev/null\x00"+realPath(sock)) {
		t.Errorf("existing socket not covered: %q", argv)
	}
	if strings.Contains(joined, "/nonexistent/kiln/bus") {
		t.Errorf("missing socket bound: %q", argv)
	}
	if strings.Index(joined, "--ro-bind\x00/dev/null\x00"+realPath(sock)) < strings.Index(joined, "--bind\x00"+realPath(ws)) {
		t.Error("socket hidden before the writable roots are mounted")
	}
}
