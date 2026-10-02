package settings

import (
	"slices"
	"testing"
)

// A destination named by option (-t, --target-directory and their other
// spellings) is written, and so is each source's name inside it; the
// sources are read, or moved.
func TestBashWrites_TargetDirectoryOption(t *testing.T) {
	cwd := t.TempDir()
	for cmd, want := range map[string][]string{
		"cp -t /d a":                 {"/d", "/d/a"},
		"cp -tdir a":                 {cwd + "/dir", cwd + "/dir/a"},
		"cp -vt /d a b":              {"/d", "/d/a", "/d/b"},
		"cp --target-directory=/d a": {"/d", "/d/a"},
		"cp --target-directory /d a": {"/d", "/d/a"},
		"cp --target=/d a":           {"/d", "/d/a"},
		"cp a " + cwd + "/x -t /d":   {"/d", "/d/a", "/d/x"},
		"ln -st /d /etc/passwd":      {"/d", "/d/passwd"},
		"install -m 0755 -t /d a":    {"/d", "/d/a"},
		"install -vt /d a":           {"/d", "/d/a"},
		"mv -t /d a":                 {"/d", cwd + "/a", "/d/a"},
		"cp -S .bak a /d":            {"/d"},
		"cp -St a /d":                {"/d"}, // -S takes "t" as its suffix
		"cp a b":                     {cwd + "/b"},
	} {
		writes, complete, _ := BashAutoModeWrites(cmd, cwd)
		if !complete {
			t.Errorf("%q: not complete", cmd)
		}
		slices.Sort(writes)
		w := slices.Clone(want)
		slices.Sort(w)
		if !slices.Equal(writes, w) {
			t.Errorf("%q: writes %q, want %q", cmd, writes, w)
		}
	}
}

// Programs whose written files kiln does not name make the analysis
// incomplete; ones it models do not.
func TestBashAutoModeWrites_UnmodelledWriters(t *testing.T) {
	cwd := t.TempDir()
	for cmd, wantComplete := range map[string]bool{
		"curl https://x.example":            false,
		"wget -qO- https://x.example | cat": false,
		"tar -xf a.tar":                     false,
		"unzip a.zip":                       false,
		"git clone https://x.example/r":     false,
		"git -C sub worktree add ../w":      false,
		"git status":                        true,
		"git log --oneline":                 true,
		"cat a > b":                         true,
		"dd if=a of=b":                      true,
	} {
		if _, complete, _ := BashAutoModeWrites(cmd, cwd); complete != wantComplete {
			t.Errorf("%q: complete = %v, want %v", cmd, complete, wantComplete)
		}
	}
}
