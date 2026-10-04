package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// Cache variables are set only where the user has not set them: in
// kiln's environment (empty counts as unset), the settings env, or for
// Go's variables Go's env file.
func TestCacheEnvRespectsUserSettings(t *testing.T) {
	goenv := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(goenv, []byte("GOMODCACHE=/somewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOENV", goenv)
	t.Setenv("PIP_CACHE_DIR", "/user/pip")
	t.Setenv("GOCACHE", "")
	t.Setenv("npm_config_cache", "")
	env := cacheEnv("/t", userSetEnv(map[string]bool{"YARN_CACHE_FOLDER": true}))
	if env["GOCACHE"] != "/t/cache/go-build" || env["npm_config_cache"] != "/t/cache/npm" || env["GOTMPDIR"] != "/t" {
		t.Errorf("unset variables not pointed at kiln's cache: %v", env)
	}
	for _, k := range []string{"PIP_CACHE_DIR", "YARN_CACHE_FOLDER", "GOMODCACHE"} {
		if v, ok := env[k]; ok {
			t.Errorf("%s was set by the user, kiln set it to %q", k, v)
		}
	}
}
