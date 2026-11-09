package mods

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Not parallel: $.env.set mutates the process environment.
func TestLocalTierFsStoreEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "MARK.md"), []byte("# mark\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "MARK.md"), filepath.Join(dir, "lnk")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KILN_MODS_TEST_ENV_SET", "") // restores the variable on cleanup
	os.Unsetenv("KILN_MODS_TEST_ENV_SET")

	// The harness stubs LookupEnv when nil; os.LookupEnv is the engine's default.
	h := newHarness(t, Options{Cwd: dir, CacheDir: t.TempDir(), LookupEnv: os.LookupEnv})
	h.load(t, "localtier")
	h.e.SessionStart(ctxT(t))
	got := runCommand(t, h, "local", "")

	type stat struct {
		Kind     string  `json:"kind"`
		Size     float64 `json:"size"`
		MtimeMs  float64 `json:"mtimeMs"`
		IsLink   bool    `json:"isLink"`
		RealPath string  `json:"realPath"`
	}
	var out struct {
		Read     string `json:"read"`
		File     stat   `json:"file"`
		Dir      stat   `json:"dir"`
		Link     stat   `json:"link"`
		Resolved stat   `json:"resolved"`
		Anc      []struct {
			Dir     string `json:"dir"`
			Name    string `json:"name"`
			Content string `json:"content"`
			Parts   []struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			} `json:"parts"`
		} `json:"anc"`
		Keys1    []string `json:"keys1"`
		Keys2    []string `json:"keys2"`
		A        float64  `json:"a"`
		Gone     bool     `json:"gone"`
		EnvSet   string   `json:"envSet"`
		EnvUnset bool     `json:"envUnset"`
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("local = %q: %v", got, err)
	}
	if out.Read != "written" {
		t.Errorf("fs.write/read = %q", out.Read)
	}
	if out.File.Kind != "file" || out.File.Size != 7 || out.File.MtimeMs == 0 || out.File.IsLink || out.File.RealPath != "" {
		t.Errorf("stat file = %+v", out.File)
	}
	if out.Dir.Kind != "dir" {
		t.Errorf("stat dir = %+v", out.Dir)
	}
	if out.Link.Kind != "other" || !out.Link.IsLink || out.Link.RealPath != "" {
		t.Errorf("stat link = %+v", out.Link)
	}
	wantReal, _ := filepath.EvalSymlinks(filepath.Join(dir, "MARK.md"))
	if out.Resolved.RealPath != wantReal {
		t.Errorf("stat resolve realPath = %q, want %q", out.Resolved.RealPath, wantReal)
	}
	if len(out.Anc) != 1 || out.Anc[0].Dir != dir || out.Anc[0].Name != "MARK.md" || out.Anc[0].Content != "# mark\n" ||
		len(out.Anc[0].Parts) != 1 || out.Anc[0].Parts[0].Path != filepath.Join(dir, "MARK.md") {
		t.Errorf("fs.ancestors = %+v", out.Anc)
	}
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(out.Keys1, want) {
		t.Errorf("store.keys = %v, want %v", out.Keys1, want)
	}
	if want := []string{"a", "c"}; !reflect.DeepEqual(out.Keys2, want) {
		t.Errorf("store.keys after delete = %v, want %v", out.Keys2, want)
	}
	if out.A != 10 || !out.Gone {
		t.Errorf("store a = %v, b gone = %v", out.A, out.Gone)
	}
	if out.EnvSet != "v1" || !out.EnvUnset {
		t.Errorf("env.set round trip = %q, unset = %v", out.EnvSet, out.EnvUnset)
	}
	if strings.Contains(h.logs.String(), "is not available in kiln") {
		t.Errorf("a $ call was rejected:\n%s", h.logs)
	}
}
