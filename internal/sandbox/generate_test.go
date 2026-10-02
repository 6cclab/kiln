package sandbox

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func testPlan(t *testing.T) (Plan, string) {
	t.Helper()
	dir := t.TempDir()
	ws := filepath.Join(dir, "ws")
	home := filepath.Join(dir, "home")
	tmp := filepath.Join(dir, "tmp")
	for _, d := range []string{ws, home, tmp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{
		DenyWrite: []Rule{{Path: filepath.Join(ws, "Secrets"), Segs: []string{"*.pem"}}},
		DenyRead:  []Rule{{Path: filepath.Join(home, ".aws")}},
		AllowRead: []Rule{{Path: filepath.Join(home, ".aws", "config")}},
	}
	return buildPlan(cfg, ws, []string{ws}, tmp, home, 4242, 0), ws
}

// TestSeatbeltProfileShape pins the parts of the generated profile the
// security of the macOS sandbox rests on.
func TestSeatbeltProfileShape(t *testing.T) {
	p, ws := testPlan(t)
	prof, err := seatbeltProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	must := []string{
		"(deny default)",
		"(allow file-read*)",
		`(allow network-outbound (remote ip "localhost:4242"))`,
		"(subpath " + `"` + realPath(ws) + `")`,
		"(deny file-write* ",
	}
	for _, m := range must {
		if !strings.Contains(prof, m) {
			t.Errorf("profile lacks %q", m)
		}
	}
	mustNot := []string{"(allow default)", "(allow network*)", "(allow network-outbound)\n", "appleevent-send", "com.apple.trustd.agent", "launchservicesd"}
	for _, m := range mustNot {
		if strings.Contains(prof, m) {
			t.Errorf("profile has %q by default", m)
		}
	}
	// Protected paths are denied case-insensitively.
	if !strings.Contains(prof, "[hH][oO][oO][kK][sS]") {
		t.Error(".git/hooks deny is not case-folded")
	}
	// The narrower allowRead comes after the denyRead it re-opens.
	di := strings.Index(prof, "(deny file-read*")
	ai := strings.LastIndex(prof, "(allow file-read* (subpath")
	if di < 0 || ai < di {
		t.Errorf("read rule order wrong: deny at %d, allow at %d", di, ai)
	}
	// The wildcard denyWrite keeps its case and its glob.
	if !regexp.MustCompile(`\[sS\]\[eE\]\[cC\]\[rR\]\[eE\]\[tT\]\[sS\]/\[\^/\]\*\\\.\[pP\]\[eE\]\[mM\]`).MatchString(prof) {
		t.Errorf("denyWrite glob not rendered: %s", prof)
	}

	// Opt-in weakenings appear only when asked for.
	p.AppleEvents, p.WeakerNetwork, p.AllowLocalBinding = true, true, true
	p.UnixSockets = []string{"/var/run/x.sock"}
	p.MachLookup = []string{"com.example.*"}
	prof, _ = seatbeltProfile(p)
	for _, m := range []string{"(allow appleevent-send)", "com.apple.trustd.agent", `(allow network-bind (local ip "localhost:*"))`, `(path-literal "/var/run/x.sock")`, `(global-name-prefix "com.example.")`} {
		if !strings.Contains(prof, m) {
			t.Errorf("opt-in %q missing", m)
		}
	}
}

func TestSeatbeltRefusesUnrepresentablePaths(t *testing.T) {
	p, _ := testPlan(t)
	p.WriteRoots = append(p.WriteRoots, `/tmp/evil"(allow default)`)
	if _, err := seatbeltProfile(p); err == nil {
		t.Fatal("a path with a quote was rendered into the profile")
	}
}

func TestGlobRegex(t *testing.T) {
	cases := []struct {
		segs []string
		path string
		want bool
	}{
		{[]string{"**", ".env"}, "/.env", true},
		{[]string{"**", ".env"}, "/a/b/.env", true},
		{[]string{"**", ".env"}, "/a/b/x.env", false},
		{[]string{"*.pem"}, "/k.pem", true},
		{[]string{"*.pem"}, "/d/k.pem", false},
		{[]string{"[!a]x"}, "/bx", true},
		{[]string{"[!a]x"}, "/ax", false},
		{[]string{"**"}, "/anything/at/all", true},
	}
	for _, c := range cases {
		re := regexp.MustCompile("^/base" + globRegex(c.segs) + "(/.*)?$")
		if got := re.MatchString("/base" + c.path); got != c.want {
			t.Errorf("%v vs %s = %v, want %v (re %s)", c.segs, c.path, got, c.want, re)
		}
	}
}

// TestProtectedPaths lists the Claude Code protected paths for a root.
func TestProtectedPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	rules, literal := protectedPaths([]string{root}, "/h", nil)
	have := map[string]bool{}
	for _, r := range rules {
		have[r.Path] = true
	}
	for _, p := range []string{
		filepath.Join(root, ".claude", "settings.json"),
		filepath.Join(root, ".claude", "settings.local.json"),
		filepath.Join(root, ".claude", "hooks"),
		filepath.Join(root, ".claude", "skills"),
		filepath.Join(filepath.Dir(root), ".claude", "settings.json"), // a parent's too
		filepath.Join(root, ".mcp.json"),
		filepath.Join(root, ".kiln"),
		filepath.Join(root, ".bashrc"),
		filepath.Join(root, ".zshrc"),
		filepath.Join(root, ".gitconfig"),
		filepath.Join(root, ".vscode"),
		filepath.Join(root, ".git", "hooks"),
		filepath.Join(root, ".git", "config"),
		filepath.Join(root, "HEAD"),
		"/h/.claude", "/h/.claude.json", "/h/.kiln", "/h/.harness",
	} {
		if !have[p] {
			t.Errorf("not protected: %s", p)
		}
	}
	if have[filepath.Join(root, "config")] {
		t.Error("a config directory with no HEAD beside it is an ordinary directory")
	}
	if strings.Join(literal, "\n") == "" || !contains(literal, filepath.Join(root, ".git")) || !contains(literal, filepath.Join(root, ".claude")) {
		t.Errorf("literal holds = %v", literal)
	}

	// With a HEAD at the top level, config and hooks there are protected.
	if err := os.WriteFile(filepath.Join(root, "HEAD"), []byte("ref: x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rules, _ = protectedPaths([]string{root}, "", nil)
	have = map[string]bool{}
	for _, r := range rules {
		have[r.Path] = true
	}
	if !have[filepath.Join(root, "config")] || !have[filepath.Join(root, "hooks")] {
		t.Error("bare-repository files not protected beside a HEAD")
	}
}

// A linked worktree may write its shared git directory, except its hooks
// and config.
func TestWorktreeGitDirs(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "main", ".git")
	wt := filepath.Join(dir, "wt")
	gitdir := filepath.Join(main, "worktrees", "wt")
	for _, d := range []string{gitdir, wt} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644)
	os.WriteFile(filepath.Join(gitdir, "commondir"), []byte("../..\n"), 0o644)
	got := worktreeGitDirs(wt)
	if len(got) != 2 || got[0] != gitdir || got[1] != main {
		t.Fatalf("worktreeGitDirs = %v", got)
	}
	p := buildPlan(Config{}, wt, []string{wt}, "", "", 0, 0)
	if !contains(p.WriteRoots, main) {
		t.Errorf("shared git dir not writable: %v", p.WriteRoots)
	}
	denied := map[string]bool{}
	for _, r := range p.DenyWrite {
		denied[r.Path] = true
	}
	if !denied[filepath.Join(main, "hooks")] || !denied[filepath.Join(main, "config")] {
		t.Error("shared hooks/config not protected")
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// TestBwrapArgs checks the Linux argv: a read-only root, writable roots
// bound read-write after it, protected paths read-only after those, no
// network namespace exit but the relay, and the command passed as an
// argument rather than spliced into the script.
func TestBwrapArgs(t *testing.T) {
	p, ws := testPlan(t)
	gitHooks := filepath.Join(ws, ".git", "hooks")
	if err := os.MkdirAll(gitHooks, 0o755); err != nil {
		t.Fatal(err)
	}
	p, _ = func() (Plan, string) {
		q := buildPlan(Config{DenyRead: p.DenyRead, AllowRead: p.AllowRead}, ws, []string{ws}, p.TmpDir, filepath.Dir(p.TmpDir)+"/home", 4242, 0)
		q.Unset = []string{"GITHUB_TOKEN"}
		return q, ws
	}()
	cmd := `echo "$(whoami)"; rm -rf '/x'`
	argv, err := bwrapArgs(p, "/usr/bin/bwrap", "/usr/bin/socat", "/bin/bash", cmd, []bwrapBridge{{Port: 4242, Socket: "/tmp/kiln-net-1/proxy.sock"}})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, "\x00")
	idx := func(seq ...string) int { return strings.Index(joined, strings.Join(seq, "\x00")) }
	for _, want := range [][]string{
		{"/usr/bin/bwrap", "--new-session", "--die-with-parent", "--unshare-pid", "--unshare-net"},
		{"--ro-bind", "/", "/"},
		{"--proc", "/proc"},
		{"--bind", realPath(ws), realPath(ws)},
		{"--ro-bind", realPath(gitHooks), realPath(gitHooks)},
		{"--bind", "/tmp/kiln-net-1", "/tmp/kiln-net-1"},
		{"--setenv", "HTTPS_PROXY", "http://127.0.0.1:4242"},
		{"--unsetenv", "GITHUB_TOKEN"},
		{"--chdir", ws},
	} {
		if idx(want...) < 0 {
			t.Errorf("argv lacks %q", want)
		}
	}
	if !(idx("--ro-bind", "/", "/") < idx("--bind", realPath(ws)) && idx("--bind", realPath(ws)) < idx("--ro-bind", realPath(gitHooks))) {
		t.Error("mount order wrong: / ro, then roots rw, then protected ro")
	}
	if argv[len(argv)-1] != cmd || argv[len(argv)-2] != "/bin/bash" {
		t.Errorf("command not passed as $1: %q", argv[len(argv)-3:])
	}
	script := argv[len(argv)-3]
	if !strings.Contains(script, "TCP-LISTEN:4242,bind=127.0.0.1") || !strings.Contains(script, "UNIX-CONNECT:'/tmp/kiln-net-1/proxy.sock'") {
		t.Errorf("relay script: %s", script)
	}
	if strings.Contains(script, "whoami") {
		t.Error("the command was spliced into the script")
	}

	// A denied directory is remounted read-only only after a narrower
	// allowRead inside it is bound back (bwrap makes that mount point in
	// the tmpfs; seen failing under real bwrap with "Can't mkdir").
	denied := filepath.Join(filepath.Dir(p.TmpDir), "home", ".aws")
	allowed := filepath.Join(denied, "config")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	argv, _ = bwrapArgs(p, "bwrap", "socat", "/bin/sh", "true", nil)
	joined = strings.Join(argv, "\x00")
	tmpfsAt := idx("--tmpfs", realPath(denied))
	rebindAt := idx("--ro-bind", realPath(allowed))
	remountAt := idx("--remount-ro", realPath(denied))
	if tmpfsAt < 0 || rebindAt < tmpfsAt || remountAt < rebindAt {
		t.Errorf("read rule mounts out of order: tmpfs %d, rebind %d, remount %d", tmpfsAt, rebindAt, remountAt)
	}

	p.WeakerNested = true
	argv, _ = bwrapArgs(p, "bwrap", "socat", "/bin/sh", "true", nil)
	if !strings.Contains(strings.Join(argv, " "), "--bind /proc /proc") {
		t.Error("enableWeakerNestedSandbox should bind the existing /proc")
	}
	if _, err := bwrapArgs(p, "bwrap", "", "/bin/sh", "true", []bwrapBridge{{Port: 1, Socket: "/s"}}); err == nil {
		t.Error("a network relay without socat should fail, not run unrelayed")
	}
}

// TestPlaceholders: a protected path missing on disk gets an empty
// read-only placeholder at its first missing component for the Linux
// sandbox, removed afterwards; concurrent commands share it.
func TestPlaceholders(t *testing.T) {
	ws := t.TempDir()
	m := New(Config{}, Options{Cwd: ws, GOOS: "linux"})
	p := buildPlan(Config{}, ws, []string{ws}, "", "", 0, 0)
	held1, release1, err := m.placeholders(p)
	if err != nil {
		t.Fatal(err)
	}
	claudeDir := filepath.Join(realPath(ws), ".claude")
	if !contains(held1, claudeDir) {
		t.Fatalf("no placeholder at .claude: %v", held1)
	}
	if fi, err := os.Lstat(claudeDir); err != nil || !fi.Mode().IsRegular() || fi.Size() != 0 {
		t.Fatalf(".claude placeholder: %v %v", fi, err)
	}
	_, release2, _ := m.placeholders(p)
	release1()
	if _, err := os.Lstat(claudeDir); err != nil {
		t.Error("placeholder removed while another command still holds it")
	}
	release2()
	if _, err := os.Lstat(claudeDir); err == nil {
		t.Error("placeholder left behind")
	}
}

// TestProxyDecisions: deny list first, then the allow list, then a strict
// allowlist refuses, otherwise Decide is asked and its yes lasts the
// session.
func TestProxyDecisions(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "ok") }))
	defer target.Close()
	port := target.Listener.Addr().(*net.TCPAddr).Port
	lookup := func(_ context.Context, h string) ([]netip.Addr, error) {
		switch h {
		case "public.test", "asked.test", "denied.test":
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		case "rebind.test":
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return nil, fmt.Errorf("nxdomain")
	}
	ipEntry := fmt.Sprintf("127.0.0.1:%d", port)
	p := NewProxy([]string{"public.test", "rebind.test", "*.denied.test", ipEntry}, []string{"denied.test"}, false)
	p.LookupIP = lookup
	asked := 0
	p.Decide = func(_ context.Context, host string, _ int) (bool, bool, error) {
		asked++
		return host == "asked.test", false, nil
	}
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	get := func(host string) (int, string) {
		proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", p.Port()))
		c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
		resp, err := c.Get(fmt.Sprintf("http://%s:%d/", host, port))
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get("public.test"); code != 200 || body != "ok" {
		t.Errorf("allowlisted: %d %q", code, body)
	}
	if code, body := get("denied.test"); code != 403 || !strings.Contains(body, "deniedDomains") {
		t.Errorf("denied wins over nothing: %d %q", code, body)
	}
	if code, _ := get("asked.test"); code != 200 || asked != 1 {
		t.Errorf("asked host: %d, asked %d times", code, asked)
	}
	if code, _ := get("asked.test"); code != 200 || asked != 1 {
		t.Errorf("an approval should last the session: %d, asked %d times", code, asked)
	}
	if code, _ := get("other.test"); code != 403 || asked != 2 {
		t.Errorf("refused by Decide: %d, asked %d", code, asked)
	}
	events, _ := p.EventsSince(0)
	if len(events) != 2 {
		t.Errorf("block log = %+v", events)
	}

	// A strict allowlist never asks.
	p.strict = true
	if code, _ := get("strict.test"); code != 403 || asked != 2 {
		t.Errorf("strict: %d, asked %d", code, asked)
	}
}

// TestProxyLocalAddressCheck: an allowed name that resolves only to a
// local address is refused unless the address itself is allowlisted;
// localhost may resolve to loopback.
func TestProxyLocalAddressCheck(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "ok") }))
	defer target.Close()
	port := target.Listener.Addr().(*net.TCPAddr).Port
	p := NewProxy([]string{"myapp.test", "localhost"}, nil, true)
	p.LookupIP = func(_ context.Context, h string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", p.Port()))
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	resp, err := c.Get(fmt.Sprintf("http://myapp.test:%d/", port))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 403 || !strings.Contains(string(b), "resolved to a loopback address") {
		t.Errorf("loopback name: %d %q", resp.StatusCode, b)
	}
	resp, err = c.Get(fmt.Sprintf("http://localhost:%d/", port))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("localhost may resolve to loopback: %d", resp.StatusCode)
	}
}

// TestManagerDetect: an unsupported platform, or a Linux machine without
// bwrap or socat, reports why; nothing is sandboxed then.
func TestManagerDetect(t *testing.T) {
	missing := func(string) (string, error) { return "", fmt.Errorf("not found") }
	m := New(Config{Enabled: true}, Options{Cwd: t.TempDir(), GOOS: "windows"})
	if err := m.Unavailable(); err == nil || !strings.Contains(err.Error(), "not supported on windows") {
		t.Errorf("windows: %v", err)
	}
	if m.Active() || m.WillSandbox("ls", false) || m.ForCommand("ls", false) != nil || m.OffersUnsandboxed() {
		t.Error("an unavailable sandbox must not claim to sandbox")
	}
	m = New(Config{Enabled: true}, Options{Cwd: t.TempDir(), GOOS: "linux", LookPath: missing})
	if err := m.Unavailable(); err == nil || !strings.Contains(err.Error(), "bubblewrap") {
		t.Errorf("linux without bwrap: %v", err)
	}
	m = New(Config{Enabled: true}, Options{Cwd: t.TempDir(), GOOS: "linux",
		LookPath: func(n string) (string, error) {
			if n == "bwrap" {
				return "/usr/bin/bwrap", nil
			}
			return "", fmt.Errorf("not found")
		}})
	if err := m.Unavailable(); err == nil || !strings.Contains(err.Error(), "socat") {
		t.Errorf("linux without socat: %v", err)
	}
	m = New(Config{Enabled: true, AllowUnsandboxed: true, Excluded: []string{"docker *"}}, Options{Cwd: t.TempDir(), GOOS: "linux",
		LookPath: func(n string) (string, error) { return "/usr/bin/" + n, nil },
		Probe:    func([]string) error { return nil }})
	if !m.Active() || m.Mechanism() != "bubblewrap" {
		t.Fatalf("linux with both: active=%v mech=%q err=%v", m.Active(), m.Mechanism(), m.Unavailable())
	}
	if !m.WillSandbox("ls", false) || m.WillSandbox("ls", true) || m.WillSandbox("docker ps", false) {
		t.Error("WillSandbox: dangerouslyDisableSandbox and excludedCommands not honoured")
	}
	m.cfg.AllowUnsandboxed = false
	if !m.WillSandbox("ls", true) {
		t.Error("with allowUnsandboxedCommands false, dangerouslyDisableSandbox is ignored")
	}
}
