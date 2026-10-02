package sandbox

import (
	"path/filepath"
	"testing"
)

// TestHostRules follows the allowedDomains syntax in Claude Code's
// settings reference: exact hosts, "*.x" for subdomains only, "*" for
// everything, ":port" to narrow, bracketed IPv6, a trailing dot ignored.
func TestHostRules(t *testing.T) {
	cases := []struct {
		entry string
		deny  bool
		host  string
		port  int
		want  bool
	}{
		{"github.com", false, "github.com", 443, true},
		{"github.com", false, "GitHub.com.", 22, true},
		{"github.com", false, "api.github.com", 443, false},
		{"*.npmjs.org", false, "registry.npmjs.org", 443, true},
		{"*.npmjs.org", false, "npmjs.org", 443, false},
		{"*.npmjs.org", false, "evilnpmjs.org", 443, false},
		{"api.example.com:443", false, "api.example.com", 443, true},
		{"api.example.com:443", false, "api.example.com", 80, false},
		{"*", false, "anything.test", 1, true},
		{"127.0.0.1:8080", false, "127.0.0.1", 8080, true},
		{"127.0.0.1:8080", false, "127.0.0.1", 8081, false},
		{"[::1]", false, "::1", 9, true},
		{"[::1]:443", false, "::1", 80, false},
		{"example.com.", true, "example.com", 443, true},
		{"example.*", false, "example.com", 443, false}, // wildcard elsewhere: no effect
		{"10.0.0.1", false, "ten.example", 443, false},  // an IP never matches a name
		{"github.com", false, "140.82.112.3", 443, false},
		// Ambiguous unbracketed IPv6: a deny list blocks every reading, an
		// allow list never more than the address-and-port reading.
		{"2001:db8::1:443", true, "2001:db8::1:443", 80, true},
		{"2001:db8::1:443", true, "2001:db8::1", 443, true},
		{"2001:db8::1:443", false, "2001:db8::1:443", 80, false},
		{"2001:db8::1:443", false, "2001:db8::1", 443, true},
	}
	for _, c := range cases {
		got := anyMatches(parseHostRules([]string{c.entry}, c.deny), c.host, c.port)
		if got != c.want {
			t.Errorf("%q (deny=%v) vs %s:%d = %v, want %v", c.entry, c.deny, c.host, c.port, got, c.want)
		}
	}
}

// TestExcluded follows sandbox.excludedCommands as Claude Code's settings
// reference describes it: every command in the call has to match, the
// call's text is matched, and some shapes stay sandboxed.
func TestExcluded(t *testing.T) {
	pats := []string{"docker *", "npm ci", "git *"}
	cases := []struct {
		cmd  string
		want bool
	}{
		{"docker compose up -d", true},
		{"docker", true},
		{"npm ci", true},
		{"npm ci --silent", false}, // no wildcard: exact
		{"npm ci && docker build .", true},
		{"npm test && docker build .", false}, // every command must match
		{"docker build . 2>&1 | docker load", true},
		{"docker build . > build.log", false}, // a redirect to a file
		{"cd build && docker compose up", false},
		{"docker run $(cat img)", false},
		{"(docker ps)", false},
		{"if true; then docker ps; fi", false},
		{"$DOCKER ps", false},
		{"sudo docker ps", false},
		{"FOO=1 docker ps", false},
		{"docker ps &", false},
		{"/usr/local/bin/docker ps", false}, // the text is matched
		{"docker run -v *.txt", false},      // a glob expands to something else
		{"git clone https://x/y vendor/lib", true},
		{"git clone https://x/y ~/tools", false},
		{"git clone https://x/y /opt/tools", false},
		{"git clone https://x/y ../escape", false},
		{"git worktree add ../wt", false},
		{"git -C /elsewhere status", false},
		{"git status", true},
		{"docker ps; rm -rf x", false},
		{"", false},
		{"docker ps 'unterminated", false},
	}
	for _, c := range cases {
		if got := excluded(c.cmd, pats); got != c.want {
			t.Errorf("excluded(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
	if excluded("docker ps", nil) {
		t.Error("nothing is excluded without entries")
	}
}

func TestCriticalRemoval(t *testing.T) {
	home := "/Users/me"
	cwd := "/Users/me/proj"
	roots := []string{cwd, "/data/extra"}
	cases := []struct {
		cmd  string
		want bool
	}{
		{"rm -rf /", true},
		{"rm -rf /usr", true},
		{"rm -rf ~", true},
		{"rm -rf .", true},
		{"rm -rf ..", true},
		{"rm -rf " + cwd, true},
		{"rm -rf *", true},
		{"rm -rf ./*", true},
		{"rm -rf /data/extra/*", true},
		{`rm -rf "$DIR"/*`, true},
		{"rm -rf $TARGET", true},
		{"rmdir ~", true},
		{"echo ok && rm -rf ~/", true},
		{"rm -rf build", false},
		{"rm -f src/*.o", false},
		{"rm -- -weird-name", false},
		{"echo rm -rf /", false},
		{"ls /", false},
	}
	for _, c := range cases {
		if got := criticalRemoval(c.cmd, cwd, home, roots); got != c.want {
			t.Errorf("criticalRemoval(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

func TestPathRuleSplitsAtWildcard(t *testing.T) {
	r := pathRule("/home/u/**/.env")
	if r.Path != "/home/u" || len(r.Segs) != 2 || r.Segs[0] != "**" || r.Segs[1] != ".env" {
		t.Errorf("pathRule = %+v", r)
	}
	if r := pathRule(filepath.Join("/a", "b")); r.glob() {
		t.Errorf("plain path became a glob: %+v", r)
	}
}
