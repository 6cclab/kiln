package learning

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFingerprintSeparatesFileBoundariesAndKinds(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	write := func(root, name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	hash := func(root string) string {
		t.Helper()
		got, err := Fingerprint(root, []byte(`{"prompt":"test"}`))
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	// The old path-newline-content encoding hashed these different fixtures
	// identically: a\nxb\nY. A promotion must never accept that pairing.
	write(a, "a", "xb\nY")
	write(b, "a", "x")
	write(b, "b", "Y")
	if hash(a) == hash(b) {
		t.Fatal("file boundaries collide")
	}
	before := hash(a)
	if err := os.Chmod(filepath.Join(a, "a"), 0700); err != nil {
		t.Fatal(err)
	}
	if hash(a) == before {
		t.Fatal("executable bit ignored")
	}
	before = hash(a)
	if err := os.Mkdir(filepath.Join(a, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	if hash(a) == before {
		t.Fatal("empty directory ignored")
	}
	write(b, "a", "xb\nY")
	if err := os.Remove(filepath.Join(b, "b")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(b, "a"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(b, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	if hash(a) != hash(b) {
		t.Fatal("identical fixtures depend on absolute root")
	}
}
