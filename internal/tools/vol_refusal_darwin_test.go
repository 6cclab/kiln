//go:build darwin

package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/andrepato/harness/internal/execenv"
)

// TestFileToolsRefuseVolPaths: /.vol/<dev>/<inode> opens a file by inode
// number, a spelling no permission rule names; read, write and edit refuse
// it, directly or through a link.
func TestFileToolsRefuseVolPaths(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(file, []byte("TOPSECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	var st syscall.Stat_t
	if err := syscall.Stat(file, &st); err != nil {
		t.Fatal(err)
	}
	vol := fmt.Sprintf("/.vol/%d/%d", st.Dev, st.Ino)
	if _, err := os.Stat(vol); err != nil {
		t.Skip("no /.vol here")
	}
	if err := os.Symlink(vol, filepath.Join(dir, "vlink")); err != nil {
		t.Fatal(err)
	}
	env := execenv.New(dir)
	for _, p := range []string{vol, "/.VOL" + vol[len("/.vol"):], "vlink"} {
		r := execTool(t, ReadTool(env), map[string]any{"path": p})
		if !r.IsError || strings.Contains(resultText(r), "TOPSECRET") || !strings.Contains(resultText(r), "/.vol") {
			t.Errorf("read %s: %q", p, resultText(r))
		}
		w := execTool(t, WriteTool(env), map[string]any{"path": p, "content": "x"})
		if !w.IsError {
			t.Errorf("write %s was not refused", p)
		}
		e := execTool(t, EditTool(env), map[string]any{"path": p, "edits": []any{map[string]any{"oldText": "TOPSECRET", "newText": "x"}}})
		if !e.IsError {
			t.Errorf("edit %s was not refused", p)
		}
	}
	if data, _ := os.ReadFile(file); string(data) != "TOPSECRET" {
		t.Errorf("the file was changed: %q", data)
	}
}
