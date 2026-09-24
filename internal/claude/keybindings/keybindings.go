package keybindings

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/andrepato/harness/internal/claude/paths"
)

// Result is the outcome of loading ~/.claude/keybindings.json.
type Result struct {
	// Loaded reports whether a user file was found and applied.
	Loaded bool
	Path   string
	// Bindings maps action name to key, from the raw file. Data-only: the
	// TUI applies it later.
	Bindings map[string]string
	// Conflicts holds human-readable conflict descriptions, reported never
	// resolved: a user who bound two actions to the same key made a
	// mistake only they can settle.
	Conflicts []string
	// Error is set when the file existed but could not be used.
	Error string
}

// Path returns ~/.claude/keybindings.json.
func Path() string {
	return paths.KeybindingsPath()
}

// Load reads and validates a keybindings file. An absent file is normal
// and returns Loaded=false with no error; defaults still apply. A file
// that exists but cannot be parsed or is not a JSON object reports an
// error instead of silently falling back, since silent fallback looks
// identical to the overrides simply not working.
func Load(path string) Result {
	result := Result{Path: path}

	raw, err := os.ReadFile(path)
	if err != nil {
		// Absent is the normal case, not an error.
		return result
	}

	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		result.Error = fmt.Sprintf("could not parse %s: %v", path, err)
		return result
	}

	// A JSON array decodes into []any, which is not a map; guard against
	// it explicitly rather than only checking for nil.
	obj, ok := value.(map[string]any)
	if !ok || value == nil {
		result.Error = fmt.Sprintf("%s must contain a JSON object mapping actions to keys", path)
		return result
	}

	bindings := map[string]string{}
	byKey := map[string][]string{}
	for action, v := range obj {
		key, ok := v.(string)
		if !ok {
			continue
		}
		bindings[action] = key
		byKey[key] = append(byKey[key], action)
	}

	result.Loaded = true
	result.Bindings = bindings

	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		if len(byKey[k]) > 1 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		actions := byKey[k]
		sort.Strings(actions)
		conflict := fmt.Sprintf("%s is bound to %s", k, joinAnd(actions))
		result.Conflicts = append(result.Conflicts, conflict)
	}

	return result
}

func joinAnd(items []string) string {
	if len(items) == 0 {
		return ""
	}
	if len(items) == 1 {
		return items[0]
	}
	out := items[0]
	for _, item := range items[1:] {
		out += " and " + item
	}
	return out
}
