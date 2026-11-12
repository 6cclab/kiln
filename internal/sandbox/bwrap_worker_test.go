package sandbox

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bindPairs returns each "--flag src dst" mount in argv.
func bindPairs(argv []string) [][3]string {
	var out [][3]string
	for i := 0; i+2 < len(argv); i++ {
		switch argv[i] {
		case "--bind", "--ro-bind", "--symlink":
			out = append(out, [3]string{argv[i], argv[i+1], argv[i+2]})
		}
	}
	return out
}

// A private-root plan never mounts the host's "/" and mounts nothing but the
// system directories, the write roots and the read-only Git metadata, while
// keeping the PID, session and network isolation the descendant guarantee
// relies on. Rules about paths the command cannot see are dropped, not
// mounted (which would expose them).
func TestBwrapPrivateRootMountsOnlyAllowlist(t *testing.T) {
	base := realPath(t.TempDir())
	child, tmp, parent := filepath.Join(base, "child"), filepath.Join(base, "tmp"), filepath.Join(base, "parent")
	gitDir := filepath.Join(parent, ".git")
	for _, d := range []string{child, tmp, gitDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	hidden := filepath.Join(base, "agent.sock") // not under any mount
	if err := os.WriteFile(hidden, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	gitSock := filepath.Join(gitDir, "fsmonitor--daemon.ipc")
	if err := os.WriteFile(gitSock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p := Plan{
		Cwd: child, PrivateRoot: true,
		WriteRoots:     []string{child, tmp},
		ReadOnlyMounts: []string{gitDir},
		DenyWrite:      []Rule{{Path: parent}, {Path: gitDir}},
		DenyRead:       []Rule{{Path: filepath.Join(parent, "secret")}},
		HiddenSockets:  []string{hidden, gitSock},
		Env:            map[string]string{"TMPDIR": tmp},
	}
	argv, err := bwrapArgs(p, "/usr/bin/bwrap", "", "/bin/sh", "true", nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := " " + strings.Join(argv, " ") + " "
	for _, want := range []string{" --new-session ", " --die-with-parent ", " --unshare-pid ", " --unshare-net "} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv lacks%s: %v", want, argv)
		}
	}
	allowed := map[string]bool{child: true, tmp: true, gitDir: true, "/dev/null": true}
	for _, d := range workerSystemDirs {
		allowed[d] = true
	}
	for _, m := range bindPairs(argv) {
		if m[2] == "/" {
			t.Errorf("host root mounted: %v", m)
		}
		if m[0] == "--symlink" {
			continue
		}
		if !allowed[m[2]] && m[2] != hidden && m[2] != gitSock && m[2] != child && !strings.HasPrefix(m[2], child+"/") {
			t.Errorf("unexpected mount %v", m)
		}
		if m[2] == parent || m[2] == hidden {
			t.Errorf("a path outside the private root was mounted: %v", m)
		}
	}
	// The Git directory's socket is covered, the unreachable one is not mentioned.
	if !strings.Contains(joined, " --ro-bind /dev/null "+gitSock+" ") {
		t.Errorf("fsmonitor socket not hidden: %v", argv)
	}
	if strings.Contains(joined, hidden) {
		t.Errorf("a socket outside the root was mounted over: %v", argv)
	}
	if strings.Contains(joined, filepath.Join(parent, "secret")) {
		t.Errorf("an invisible deny-read path was mounted: %v", argv)
	}
}

func TestBwrapPrivateRootRequiresFilesystemRestrictions(t *testing.T) {
	if _, err := bwrapArgs(Plan{PrivateRoot: true, FilesystemDisabled: true}, "bwrap", "", "/bin/sh", "true", nil); err == nil {
		t.Fatal("private root accepted without filesystem restrictions")
	}
}

func TestWorkerHiddenSocketsFindsMetadataSockets(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "kg-") // short: Unix socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.MkdirAll(filepath.Join(dir, "objects"), 0o700); err != nil {
		t.Fatal(err)
	}
	listen := func(path string) {
		ln, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
	}
	fsmon := filepath.Join(dir, "fsmonitor--daemon.ipc")
	listen(fsmon)
	listen(filepath.Join(dir, "objects", "s")) // objects are not scanned
	socks, err := workerHiddenSockets([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range socks {
		found = found || s == fsmon
		if strings.HasPrefix(s, filepath.Join(dir, "objects")) {
			t.Errorf("objects scanned: %s", s)
		}
	}
	if !found {
		t.Errorf("fsmonitor socket not found in %v", socks)
	}
}
