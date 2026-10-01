package execenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRealPath(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := func(to, at string) {
		t.Helper()
		if err := os.Symlink(to, filepath.Join(dir, at)); err != nil {
			t.Fatal(err)
		}
	}
	link(filepath.Join(target, "missing.txt"), "dangle")
	link("target", "reldir")
	link("dangle", "hop")
	link("loopA", "loopB")
	link("loopB", "loopA")

	cases := []struct {
		in, want string
		ok       bool
	}{
		{filepath.Join(dir, "dangle"), filepath.Join(target, "missing.txt"), true},
		{filepath.Join(dir, "hop"), filepath.Join(target, "missing.txt"), true},
		{filepath.Join(dir, "reldir", "new", "file"), filepath.Join(target, "new", "file"), true},
		{filepath.Join(dir, "reldir", "..", "x"), filepath.Join(dir, "x"), true},
		{filepath.Join(dir, "plain"), filepath.Join(dir, "plain"), true},
		{filepath.Join(dir, "loopA", "x"), filepath.Join(dir, "loopA", "x"), false},
	}
	for _, c := range cases {
		got, ok := RealPath(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("RealPath(%s) = %s, %v; want %s, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}
