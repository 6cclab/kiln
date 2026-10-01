//go:build darwin

package execenv

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestCanonicalPath_DarwinAliases: the spellings macOS opens as one file
// all canonicalise to the same path.
func TestCanonicalPath_DarwinAliases(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "Secrets", "k")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("APFS case folding (long s)", func(t *testing.T) {
		alias := filepath.Join(dir, "ſecrets", "K")
		if _, err := os.Stat(alias); err != nil {
			t.Skip("this volume does not fold ſ to s")
		}
		if got := CanonicalPath(alias); got != file {
			t.Errorf("CanonicalPath(%s) = %s, want %s", alias, got, file)
		}
	})
	t.Run("firmlink", func(t *testing.T) {
		alias := "/System/Volumes/Data" + file
		if _, err := os.Stat(alias); err != nil {
			t.Skip("no /System/Volumes/Data firmlink here")
		}
		if got := CanonicalPath(alias); got != file {
			t.Errorf("CanonicalPath(%s) = %s, want %s", alias, got, file)
		}
	})
	t.Run("/.vol inode path", func(t *testing.T) {
		var st syscall.Stat_t
		if err := syscall.Stat(file, &st); err != nil {
			t.Fatal(err)
		}
		alias := fmt.Sprintf("/.vol/%d/%d", st.Dev, st.Ino)
		if _, err := os.Stat(alias); err != nil {
			t.Skip("no /.vol here")
		}
		if got := CanonicalPath(alias); got != file {
			t.Errorf("CanonicalPath(%s) = %s, want %s", alias, got, file)
		}
	})
	t.Run("missing tail kept", func(t *testing.T) {
		alias := "/System/Volumes/Data" + filepath.Join(dir, "Secrets", "new", "f")
		if _, err := os.Stat("/System/Volumes/Data" + dir); err != nil {
			t.Skip("no firmlink here")
		}
		want := filepath.Join(dir, "Secrets", "new", "f")
		if got := CanonicalPath(alias); got != want {
			t.Errorf("CanonicalPath(%s) = %s, want %s", alias, got, want)
		}
	})
}
