package sandbox

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Tool caches inside the sandbox: kiln's divergence from Claude Code,
// which leaves the real cache directories (~/Library/Caches/go-build,
// ~/.npm, ~/.cache/pip, ...) unwritable, so `go build` or `npm install`
// fail inside its sandbox. kiln points the well-known cache variables of a
// sandboxed command at a per-user cache under its temp root
// (<temp root>/cache/<tool>), which the sandbox may write. The real cache
// directories stay unwritable. A variable the user set (in kiln's
// environment, the settings "env", or for Go its env file) is left alone,
// even when that makes the tool fail inside the sandbox.

// toolCaches maps each cache variable to its directory under the cache
// root. GOTMPDIR is not a cache, but Go defaults it to the system temp
// directory; it gets the sandbox's own ($TMPDIR) instead, see cacheEnv.
var toolCaches = []struct{ env, dir string }{
	{"GOCACHE", "go-build"},
	{"GOMODCACHE", "go-mod"},
	{"npm_config_cache", "npm"},
	{"npm_config_store_dir", "pnpm-store"},
	{"YARN_CACHE_FOLDER", "yarn"},
	{"BUN_INSTALL_CACHE_DIR", "bun"},
	{"DENO_DIR", "deno"},
	{"PIP_CACHE_DIR", "pip"},
	{"UV_CACHE_DIR", "uv"},
	{"POETRY_CACHE_DIR", "poetry"},
	{"PRE_COMMIT_HOME", "pre-commit"},
	{"COMPOSER_CACHE_DIR", "composer"},
	{"CCACHE_DIR", "ccache"},
	{"XDG_CACHE_HOME", "xdg"},
}

// cacheEnv returns the cache variables for a sandboxed command whose temp
// directory is tmpDir: those the user has not set (userSet).
func cacheEnv(tmpDir string, userSet func(string) bool) map[string]string {
	out := map[string]string{}
	root := filepath.Join(tmpDir, "cache")
	for _, c := range toolCaches {
		if !userSet(c.env) {
			out[c.env] = filepath.Join(root, c.dir)
		}
	}
	if !userSet("GOTMPDIR") {
		out["GOTMPDIR"] = tmpDir
	}
	return out
}

// userSetEnv reports whether the user set a variable: non-empty in kiln's
// environment, present in the settings env, or, for Go's variables, in
// Go's env file (go env -w).
func userSetEnv(settingsEnv map[string]bool) func(string) bool {
	goEnv := goEnvFileKeys()
	return func(k string) bool {
		if v, ok := os.LookupEnv(k); ok && v != "" {
			return true
		}
		return settingsEnv[k] || (strings.HasPrefix(k, "GO") && goEnv[k])
	}
}

// goEnvFileKeys reads the keys set in Go's env file ($GOENV, else
// <user config dir>/go/env), which go env -w writes.
func goEnvFileKeys() map[string]bool {
	path := os.Getenv("GOENV")
	if path == "off" {
		return nil
	}
	if path == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return nil
		}
		path = filepath.Join(dir, "go", "env")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	keys := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, _, ok := strings.Cut(strings.TrimSpace(sc.Text()), "="); ok && k != "" && !strings.HasPrefix(k, "#") {
			keys[k] = true
		}
	}
	return keys
}

// ensureCacheRoot creates <tmpDir>/cache (0700) and reports whether it is
// a real directory this user owns. tmpDir is already private, so nobody
// else can have made it; a sandboxed command could have replaced it with
// a link, and then no cache variables are set.
func ensureCacheRoot(tmpDir string) bool {
	root := filepath.Join(tmpDir, "cache")
	if err := os.Mkdir(root, 0o700); err != nil && !os.IsExist(err) {
		return false
	}
	fi, err := os.Lstat(root)
	return err == nil && fi.IsDir() && fi.Mode()&os.ModeSymlink == 0 && ownedByMe(fi)
}
