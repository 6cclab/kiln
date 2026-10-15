package plugins

import (
	"path/filepath"
	"reflect"
	"testing"
)

// TestModulesParsedFromHooksJSON: a mod's hooks/hooks.json lists its hooks
// modules under "modules" (squad's is {"modules": ["./register.tsx"]}).
// kiln read only command hooks from it, so it loaded nothing from a mod.
// A module path that leaves the plugin root is not loaded.
func TestModulesParsedFromHooksJSON(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, "squad", "1.0.0", nil)
	writeFile(t, filepath.Join(root, "hooks", "hooks.json"),
		`{"modules": ["./register.tsx", "../escape.ts", "./register.tsx"], "hooks": {}}`)
	p := Plugin{Key: "squad@apato-plugins", Name: "squad", Marketplace: "apato-plugins", Root: root}

	want := []string{filepath.Join(root, "register.tsx")}
	if got := Modules(p); !reflect.DeepEqual(got, want) {
		t.Fatalf("Modules = %v, want %v", got, want)
	}
	if got := Hooks(p); len(got) != 0 {
		t.Fatalf("a modules-only hooks.json has no command hooks; got %v", got)
	}
}

func TestModulesNoneForCommandHooksOnly(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, "fmt", "1.0.0", nil)
	writeFile(t, filepath.Join(root, "hooks", "hooks.json"),
		`{"hooks": {"PostToolUse": [{"matcher": "Edit", "hooks": [{"type": "command", "command": "true"}]}]}}`)
	if got := Modules(Plugin{Key: "fmt@m", Name: "fmt", Marketplace: "m", Root: root}); len(got) != 0 {
		t.Fatalf("Modules = %v, want none", got)
	}
}
