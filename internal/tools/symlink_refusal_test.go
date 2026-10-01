package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/execenv"
)

// TestWriteEditRefuseSymlinkPath: as in Claude Code ("Writes through a
// symlink"), edit and write refuse a path that is itself a symlink and name
// its target, so nothing is written through it, dangling or not.
func TestWriteEditRefuseSymlinkPath(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	existing := filepath.Join(outside, "real.txt")
	if err := os.WriteFile(existing, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(existing, filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(outside, "authorized_keys")
	if err := os.Symlink(missing, filepath.Join(dir, "dangle")); err != nil {
		t.Fatal(err)
	}
	env := execenv.New(dir)

	for _, c := range []struct {
		name string
		run  func() string
	}{
		{"write existing", func() string {
			r := execTool(t, WriteTool(env), map[string]any{"path": "link.txt", "content": "pwned"})
			if !r.IsError {
				t.Error("not an error")
			}
			return resultText(r)
		}},
		{"write dangling", func() string {
			r := execTool(t, WriteTool(env), map[string]any{"path": "dangle", "content": "pwned"})
			if !r.IsError {
				t.Error("not an error")
			}
			return resultText(r)
		}},
		{"edit", func() string {
			r := execTool(t, EditTool(env), map[string]any{"path": "link.txt", "edits": []any{map[string]any{"oldText": "keep", "newText": "pwned"}}})
			if !r.IsError {
				t.Error("not an error")
			}
			return resultText(r)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if text := c.run(); !strings.Contains(text, "symlink") {
				t.Errorf("message %q does not say it is a symlink", text)
			}
		})
	}
	if data, _ := os.ReadFile(existing); string(data) != "keep" {
		t.Errorf("the link's target was changed: %q", data)
	}
	if _, err := os.Stat(missing); err == nil {
		t.Error("the dangling link's target was created")
	}
}
