package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The Linux sandbox: bubblewrap (bwrap), as Claude Code uses on Linux and
// WSL2 (code.claude.com/docs/en/sandboxing, "OS-level enforcement").
//
// The command sees the host filesystem read-only, with the writable roots
// bound read-write over it and the protected paths and denyWrite entries
// bound read-only again on top. It runs in its own network namespace with
// only a loopback interface; each loopback port it may reach (kiln's proxy,
// a configured proxy port) is a socat relay inside the namespace to a Unix
// socket kiln listens on outside it. Wildcard write entries are skipped,
// as Claude Code skips them on Linux (the sandbox mounts concrete paths),
// and so are wildcard read entries, which Claude Code expands and kiln
// does not yet.

// bwrapBridge is one loopback port relayed into the namespace.
type bwrapBridge struct {
	Port   int
	Socket string // Unix socket outside the namespace
}

// bwrapArgs builds the argv that runs `shell -c command` under bwrap for
// plan p. socat is the relay binary (absolute); bridges may be empty when
// no port is reachable.
func bwrapArgs(p Plan, bwrap, socat, shell, command string, bridges []bwrapBridge) ([]string, error) {
	a := []string{bwrap, "--new-session", "--die-with-parent", "--unshare-pid", "--unshare-net"}
	if !p.FilesystemDisabled {
		a = append(a, "--ro-bind", "/", "/")
	} else {
		a = append(a, "--bind", "/", "/")
	}
	a = append(a, "--dev", "/dev")
	if p.WeakerNested {
		a = append(a, "--bind", "/proc", "/proc")
	} else {
		a = append(a, "--proc", "/proc")
	}

	if !p.FilesystemDisabled {
		for _, root := range sortedByDepth(p.WriteRoots) {
			r := realPath(root)
			if exists(r) {
				a = append(a, "--bind", r, r)
			}
		}
		// A mount point cannot be renamed or removed: binding each held
		// directory onto itself keeps it in place while it stays writable.
		for _, l := range sortedByDepth(p.DenyWriteLiteral) {
			r := realPath(l)
			if isDir(r) && underAny(r, p.WriteRoots) {
				a = append(a, "--bind", r, r)
			}
		}
		var ro []string
		for _, d := range p.DenyWrite {
			if d.glob() {
				continue
			}
			for _, s := range spellings(d.Path) {
				if exists(s) {
					ro = append(ro, realPath(s))
				}
			}
		}
		ro = append(ro, p.Placeholders...)
		for _, r := range sortedByDepth(dedupe(ro)) {
			a = append(a, "--ro-bind", r, r)
		}
		// A denied directory becomes an empty tmpfs, made read-only only
		// after any narrower allowRead inside it is bound back in: bwrap
		// creates those mount points inside the tmpfs.
		var remount []string
		for _, r := range orderedReadRules(p.DenyRead, p.AllowRead) {
			if r.rule.glob() {
				continue
			}
			path := realPath(r.rule.Path)
			if !exists(path) {
				continue
			}
			switch {
			case !r.deny:
				a = append(a, "--ro-bind", path, path)
			case isDir(path):
				a = append(a, "--tmpfs", path)
				remount = append(remount, path)
			default:
				a = append(a, "--ro-bind", "/dev/null", path)
			}
		}
		for _, path := range remount {
			a = append(a, "--remount-ro", path)
		}
	}

	for _, br := range bridges {
		dir := filepath.Dir(br.Socket)
		a = append(a, "--bind", dir, dir)
	}
	for _, k := range sortedKeys(p.Env) {
		a = append(a, "--setenv", k, p.Env[k])
	}
	for _, k := range p.Unset {
		a = append(a, "--unsetenv", k)
	}
	if p.Cwd != "" {
		a = append(a, "--chdir", p.Cwd)
	}

	script := `exec "$0" -c "$1"`
	if len(bridges) > 0 {
		if socat == "" {
			return nil, fmt.Errorf("sandbox: socat is required to reach the network proxy")
		}
		var b strings.Builder
		for _, br := range bridges {
			fmt.Fprintf(&b, "%s TCP-LISTEN:%d,bind=127.0.0.1,fork,reuseaddr UNIX-CONNECT:%s >/dev/null 2>&1 &\n",
				shellQuote(socat), br.Port, shellQuote(br.Socket))
		}
		for _, br := range bridges {
			// Wait until the relay listens, so the command's first
			// connection does not race it.
			fmt.Fprintf(&b, "i=0; while [ $i -lt 200 ] && ! (exec 3<>/dev/tcp/127.0.0.1/%d) 2>/dev/null; do sleep 0.01; i=$((i+1)); done\n", br.Port)
		}
		b.WriteString(script)
		script = b.String()
	}
	a = append(a, "--", shell, "-c", script, shell, command)
	return a, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func underAny(p string, roots []string) bool {
	for _, r := range roots {
		r = realPath(r)
		if p == r || strings.HasPrefix(p, strings.TrimSuffix(r, "/")+"/") {
			return true
		}
	}
	return false
}

// sortedByDepth orders paths shallowest first, so a deeper mount is made
// after (and over) the one containing it.
func sortedByDepth(in []string) []string {
	out := append([]string(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		return strings.Count(filepath.Clean(out[i]), "/") < strings.Count(filepath.Clean(out[j]), "/")
	})
	return out
}
