package execenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvWriteFileCreatesParents(t *testing.T) {
	dir := t.TempDir()
	env := New(dir)
	if err := env.WriteFile("nested/deep/file.txt", []byte("hi")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "nested/deep/file.txt"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "hi" {
		t.Fatalf("content = %q, want %q", data, "hi")
	}
}

func TestEnvReadFileRelativeToCwd(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := New(dir)
	data, err := env.ReadFile("a.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "contents" {
		t.Fatalf("content = %q", data)
	}
}

func TestEnvAbsolutePathExpandsHome(t *testing.T) {
	env := New(t.TempDir())
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	got := env.AbsolutePath("~/foo")
	want := filepath.Join(home, "foo")
	if got != want {
		t.Fatalf("AbsolutePath(~/foo) = %q, want %q", got, want)
	}
}

func TestEnvListDirAndStat(t *testing.T) {
	dir := t.TempDir()
	env := New(dir)
	if err := env.WriteFile("f1.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := env.CreateDir("sub", true); err != nil {
		t.Fatal(err)
	}
	entries, err := env.ListDir(".")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d: %+v", len(entries), entries)
	}
	info, err := env.Stat("f1.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Kind != KindFile || info.Size != 1 {
		t.Fatalf("info = %+v", info)
	}
}

func TestEnvExists(t *testing.T) {
	dir := t.TempDir()
	env := New(dir)
	if ok, err := env.Exists("missing"); err != nil || ok {
		t.Fatalf("Exists(missing) = %v, %v", ok, err)
	}
	if err := env.WriteFile("present", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if ok, err := env.Exists("present"); err != nil || !ok {
		t.Fatalf("Exists(present) = %v, %v", ok, err)
	}
}

func TestEnvRemove(t *testing.T) {
	dir := t.TempDir()
	env := New(dir)
	if err := env.WriteFile("gone.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := env.Remove("gone.txt", RemoveOptions{}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if ok, _ := env.Exists("gone.txt"); ok {
		t.Fatal("expected file to be removed")
	}
	// Force removing an already-missing path must not error.
	if err := env.Remove("gone.txt", RemoveOptions{Force: true}); err != nil {
		t.Fatalf("Remove with Force on missing path: %v", err)
	}
}
