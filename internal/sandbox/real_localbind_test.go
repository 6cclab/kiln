//go:build darwin

package sandbox

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

// allowLocalBinding lets a sandboxed command listen, and opens no direct
// route to loopback ports: a server
// already running on this machine is out of reach unless allowedDomains
// names it, and then only through the proxy. Without an inherited
// NO_PROXY, a localhost request goes through the proxy too.
func TestRealSandboxLocalBinding(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "host-service") }))
	defer host.Close()
	port := host.Listener.Addr().(*net.TCPAddr).Port
	t.Setenv("NO_PROXY", "localhost,127.0.0.1")

	r := newRealRig(t, Config{AllowLocalBinding: true}, nil)
	bind := func(addr string) string {
		return `python3 -c "import socket; s=socket.socket(); s.bind(('` + addr + `', 0)); s.listen(); print('bound')"`
	}
	if out, code := r.run(bind("127.0.0.1")); code != 0 || !strings.Contains(out, "bound") {
		t.Errorf("binding loopback refused: %d %q", code, out)
	}
	// Binding every interface cannot be refused: Seatbelt's local-address
	// filter takes only "*" or "localhost", and "localhost" matches any
	// local address (checked with sandbox-exec; Claude Code's docs note
	// the same exposure). Inbound is limited to the same filter.
	direct := fmt.Sprintf("curl -sS -m 5 --noproxy '*' http://127.0.0.1:%d/", port)
	if out, code := r.run(direct); code == 0 || strings.Contains(out, "host-service") {
		t.Errorf("direct connection to a host service: %q", out)
	}
	viaEnv := fmt.Sprintf("curl -sSf -m 5 http://localhost:%d/", port)
	if out, code := r.run(viaEnv); code == 0 || strings.Contains(out, "host-service") {
		t.Errorf("an unlisted localhost service through the proxy: %q", out)
	}

	r = newRealRig(t, Config{AllowLocalBinding: true, AllowedDomains: []string{fmt.Sprintf("localhost:%d", port)}}, nil)
	if out, code := r.run(viaEnv); code != 0 || !strings.Contains(out, "host-service") {
		t.Errorf("an exactly allowed localhost service through the proxy: %d %q", code, out)
	}
}
