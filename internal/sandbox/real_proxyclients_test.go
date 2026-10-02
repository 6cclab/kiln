//go:build darwin

package sandbox

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// The tools a sandboxed command uses to reach the network send the
// credential in the proxy URL kiln gives them: curl, git, npm, pip and a
// Go program each reach an allowlisted server through the proxy. The
// server records which paths it was asked for; a client that did not
// authenticate would get 407 from the proxy and never get there.
func TestRealSandboxProxyClients(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = true
		mu.Unlock()
		http.NotFound(w, r)
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	base := fmt.Sprintf("http://allowed.test:%d", port)

	goHelper := ""
	if _, err := exec.LookPath("go"); err == nil {
		dir := t.TempDir()
		src := filepath.Join(dir, "main.go")
		os.WriteFile(src, []byte(`package main
import ("net/http"; "os")
func main() { r, err := http.Get(os.Args[1]); if err != nil { panic(err) }; r.Body.Close() }
`), 0o644)
		goHelper = filepath.Join(dir, "getter")
		if out, err := exec.Command("go", "build", "-o", goHelper, src).CombinedOutput(); err != nil {
			t.Fatalf("build helper: %v %s", err, out)
		}
	}

	cfg := Config{AllowedDomains: []string{"allowed.test", fmt.Sprintf("127.0.0.1:%d", port)}, StrictAllowlist: true}
	r := newRealRig(t, cfg, map[string]string{"allowed.test": "127.0.0.1"})
	clients := map[string]string{
		"curl": "curl -s -m 10 " + base + "/curl",
		"git":  "git ls-remote " + base + "/git.git",
		"npm":  `npm_config_cache="$TMPDIR/npm" npm view kiln-probe --registry ` + base + "/npm/ --fetch-retries=0",
		"pip":  `python3 -m pip download --no-deps --no-cache-dir --disable-pip-version-check --retries 0 --trusted-host allowed.test -d "$TMPDIR/pip" --index-url ` + base + "/pip/simple/ kiln-probe",
		"go":   goHelper + " " + base + "/go",
	}
	for name, cmd := range clients {
		bin := map[string]string{"curl": "curl", "git": "git", "npm": "npm", "pip": "python3", "go": goHelper}[name]
		if bin == "" {
			t.Logf("%s: not installed, skipped", name)
			continue
		}
		if _, err := exec.LookPath(bin); err != nil && name != "go" {
			t.Logf("%s: not installed, skipped", name)
			continue
		}
		out, _ := r.run(cmd)
		mu.Lock()
		reached := false
		for path := range seen {
			if len(path) > len(name)+1 && path[1:len(name)+1] == name || path == "/"+name {
				reached = true
			}
		}
		mu.Unlock()
		if !reached {
			t.Errorf("%s did not reach the server through the proxy:\n%s", name, out)
		}
	}
}
