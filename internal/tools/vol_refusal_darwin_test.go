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
// number, a spelling no permission rule names; read, edit and write refuse
// it, saying why, and the file is neither returned nor changed. (On this
// macOS neither "/.VOL/…" nor a symlink to a /.vol path opens at all, so
// those spellings are not tested: any error would pass.)
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
	if data, err := os.ReadFile(vol); err != nil || string(data) != "TOPSECRET" {
		t.Skip("/.vol does not open the file here")
	}
	env := execenv.New(dir)
	refused := func(op string, text string) {
		t.Helper()
		if !strings.Contains(text, "/.vol") || strings.Contains(text, "TOPSECRET") {
			t.Errorf("%s %s: %q, want a /.vol refusal", op, vol, text)
		}
		if data, _ := os.ReadFile(file); string(data) != "TOPSECRET" {
			t.Fatalf("%s changed the file through %s: %q", op, vol, data)
		}
	}
	refused("read", resultText(execTool(t, ReadTool(env), map[string]any{"path": vol})))
	refused("edit", resultText(execTool(t, EditTool(env), map[string]any{"path": vol, "edits": []any{map[string]any{"oldText": "TOPSECRET", "newText": "x"}}})))
	refused("write", resultText(execTool(t, WriteTool(env), map[string]any{"path": vol, "content": "x"})))
}
