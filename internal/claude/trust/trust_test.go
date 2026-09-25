package trust

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewStore_UsesHomeHarnessDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	s, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	want := filepath.Join(home, ".harness", "trusted.json")
	if s.path != want {
		t.Errorf("path = %q, want %q", s.path, want)
	}
}

func TestIsTrusted_UntrustedByDefault(t *testing.T) {
	dir := t.TempDir()
	s := NewStoreAt(filepath.Join(dir, "trusted.json"))

	proj := t.TempDir()
	if s.IsTrusted(proj) {
		t.Error("fresh store should trust nothing")
	}
}

func TestTrust_ThenIsTrusted(t *testing.T) {
	dir := t.TempDir()
	s := NewStoreAt(filepath.Join(dir, "trusted.json"))

	proj := t.TempDir()
	if err := s.Trust(proj); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	if !s.IsTrusted(proj) {
		t.Error("expected proj to be trusted after Trust")
	}
}

func TestIsTrusted_AncestorTrustsChildren(t *testing.T) {
	dir := t.TempDir()
	s := NewStoreAt(filepath.Join(dir, "trusted.json"))

	root := t.TempDir()
	child := filepath.Join(root, "sub", "deeper")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := s.Trust(root); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	if !s.IsTrusted(child) {
		t.Error("expected child of a trusted ancestor to be trusted")
	}
	// A sibling directory sharing the root's prefix as a substring (not a
	// path-separator-bounded ancestor) must NOT be trusted.
	sibling := root + "-sibling"
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	if s.IsTrusted(sibling) {
		t.Error("sibling directory with a shared string prefix must not be trusted")
	}
}

func TestIsTrusted_UnrelatedDirNotTrusted(t *testing.T) {
	dir := t.TempDir()
	s := NewStoreAt(filepath.Join(dir, "trusted.json"))

	a := t.TempDir()
	b := t.TempDir()
	if err := s.Trust(a); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	if s.IsTrusted(b) {
		t.Error("unrelated directory must not be trusted")
	}
}

func TestTrust_Idempotent(t *testing.T) {
	dir := t.TempDir()
	s := NewStoreAt(filepath.Join(dir, "trusted.json"))

	proj := t.TempDir()
	if err := s.Trust(proj); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	if err := s.Trust(proj); err != nil {
		t.Fatalf("second Trust: %v", err)
	}
	paths, err := s.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("expected 1 trusted path after idempotent Trust, got %d: %v", len(paths), paths)
	}
}

func TestTrust_TrustingChildOfTrustedAncestorNoOps(t *testing.T) {
	dir := t.TempDir()
	s := NewStoreAt(filepath.Join(dir, "trusted.json"))

	root := t.TempDir()
	child := filepath.Join(root, "sub")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Trust(root); err != nil {
		t.Fatalf("Trust(root): %v", err)
	}
	if err := s.Trust(child); err != nil {
		t.Fatalf("Trust(child): %v", err)
	}
	paths, err := s.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("expected still 1 trusted path (child covered by root), got %d: %v", len(paths), paths)
	}
}

func TestStore_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "trusted.json")
	s := NewStoreAt(storePath)

	proj := t.TempDir()
	if err := s.Trust(proj); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	info, err := os.Stat(storePath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("trusted.json perm = %o, want 0600", perm)
	}
}

func TestIsTrusted_RelativeCwdNormalized(t *testing.T) {
	dir := t.TempDir()
	s := NewStoreAt(filepath.Join(dir, "trusted.json"))

	proj := t.TempDir()
	if err := s.Trust(proj); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	// Trailing slash and a redundant "." segment must normalize to the
	// same identity as the trusted path.
	if !s.IsTrusted(proj + "/./") {
		t.Error("expected a path differing only by trailing slash/dot segment to be trusted")
	}
}
