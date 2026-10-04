package sandbox

import (
	"testing"
)

// On macOS kiln runs /usr/bin/sandbox-exec and nothing else: a
// sandbox-exec found earlier on PATH is never probed or used to wrap.
func TestDetectUsesSystemSandboxExec(t *testing.T) {
	var probed []string
	cwd := t.TempDir()
	m := New(Config{Enabled: true}, Options{Cwd: cwd, GOOS: "darwin",
		LookPath: func(n string) (string, error) { return "/repo/bin/" + n, nil },
		Probe:    func(argv []string) error { probed = append(probed, argv[0]); return nil }})
	defer m.Close()
	if !m.Active() {
		t.Fatalf("not active: %v", m.Unavailable())
	}
	if len(probed) != 1 || probed[0] != "/usr/bin/sandbox-exec" {
		t.Errorf("probed %v, want only /usr/bin/sandbox-exec", probed)
	}
	w, err := m.ForCommand("true", false).Wrap("/bin/sh", "true", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if w.Argv[0] != "/usr/bin/sandbox-exec" {
		t.Errorf("wrapped with %q", w.Argv[0])
	}
}
