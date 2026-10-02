//go:build darwin || linux

package sandbox

import (
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A sandboxed command cannot reach the user's ssh agent (or the session
// bus, docker, ...) through its Unix socket: on Linux the socket file is
// covered by /dev/null, on macOS Seatbelt denies the connect. An
// allowUnixSockets entry for it opens it again.
func TestRealSandboxHidesAgentSockets(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	dir, err := os.MkdirTemp("/tmp", "kiln-sock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("agent-reached")) })}
	go srv.Serve(ln)
	defer srv.Close()
	t.Setenv("SSH_AUTH_SOCK", sock)
	cmd := `curl -s -m 5 --unix-socket "$SSH_AUTH_SOCK" http://agent/ ; echo " exit=$?"`

	r := newRealRig(t, Config{}, nil)
	if out, _ := r.run(cmd); strings.Contains(out, "agent-reached") {
		t.Errorf("sandboxed command reached the ssh agent socket: %s", out)
	}

	r = newRealRig(t, Config{UnixSockets: []string{sock}}, nil)
	if out, _ := r.run(cmd); !strings.Contains(out, "agent-reached") {
		t.Errorf("allowUnixSockets entry did not open the socket: %s", out)
	}
}
