package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func allowAll(string) bool { return true }

func TestDirectoryAndAbsentSourcesTrackMembership(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "pkg")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("a"), 0600); err != nil {
		t.Fatal(err)
	}
	sources, err := CaptureSourceSpecs(root, []SourceSpec{{SourceDir, "pkg"}, {SourceAbsent, "gone.go"}}, allowAll)
	if err != nil || len(sources) != 2 {
		t.Fatalf("capture: %+v %v", sources, err)
	}
	if !SourcesCurrentAt(sources, root) {
		t.Fatal("fresh sources reported stale")
	}
	for name, change := range map[string]func() error{
		"member added":   func() error { return os.WriteFile(filepath.Join(dir, "b.go"), []byte("b"), 0600) },
		"member edited":  func() error { return os.WriteFile(filepath.Join(dir, "a.go"), []byte("A"), 0600) },
		"absent appears": func() error { return os.WriteFile(filepath.Join(root, "gone.go"), []byte("x"), 0600) },
	} {
		if err := change(); err != nil {
			t.Fatal(err)
		}
		if SourcesCurrentAt(sources, root) {
			t.Fatalf("%s: stale source reported current", name)
		}
		_ = os.Remove(filepath.Join(dir, "b.go"))
		_ = os.Remove(filepath.Join(root, "gone.go"))
		if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("a"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if !SourcesCurrentAt(sources, root) {
		t.Fatal("restored tree should be current")
	}
	if err := os.Symlink("a.go", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if SourcesCurrentAt(sources, root) {
		t.Fatal("symlink member must not be current")
	}
}

func TestCaptureSourceSpecsRefusesUnsafeShapes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.go"), []byte("f"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "d"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, spec := range map[string]SourceSpec{
		"file as dir":     {SourceDir, "f.go"},
		"existing absent": {SourceAbsent, "f.go"},
		"outside":         {SourceDir, ".."},
		"unknown kind":    {"tree", "d"},
		"root as absent":  {SourceAbsent, "."},
	} {
		if _, err := CaptureSourceSpecs(root, []SourceSpec{spec}, allowAll); err == nil {
			t.Errorf("%s: captured", name)
		}
	}
	if _, err := CaptureSourceSpecs(root, []SourceSpec{{SourceDir, "d"}}, func(string) bool { return false }); err == nil {
		t.Error("denied Read captured a directory")
	}
	if _, err := CaptureSourceSpecs(root, []SourceSpec{{SourceDir, "d"}}, nil); err == nil {
		t.Error("nil Read callback captured a directory")
	}
}

func TestLegacySourceRowsWithoutKindStillWork(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("a"), 0600); err != nil {
		t.Fatal(err)
	}
	sources, err := CaptureSources(root, []string{"a.go"})
	if err != nil {
		t.Fatal(err)
	}
	// Rows written before kinds existed hold no "kind" key at all.
	if sources[0].Kind != SourceFile {
		t.Fatalf("file sources must stay the zero kind: %q", sources[0].Kind)
	}
	if !SourcesCurrentAt(sources, root) {
		t.Fatal("legacy file source reported stale")
	}
	if got := derivedFactGroup("Observed successful verification command: go test ./..."); got != "go test ./..." {
		t.Fatalf("legacy text group: %q", got)
	}
	if got := derivedFactGroup("Observed successful verification command (in a/b): go test ./..."); got != "(in a/b): go test ./..." {
		t.Fatalf("subdirectory text group: %q", got)
	}
	if derivedFactGroup("Observed successful verification command (in a/b) go test") != "" || derivedFactGroup("other") != "" {
		t.Fatal("unrelated text accepted as automatic fact")
	}
}
