package faux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzParseScriptYAML feeds arbitrary YAML documents through the same
// pipeline loadScriptFile drives (parseScriptYAML, then modelScripts and
// flattenSteps for every per-model step list), which must never panic
// regardless of how malformed or adversarial the document is.
func FuzzParseScriptYAML(f *testing.F) {
	yamlDir := filepath.Join("..", "..", "..", "testdata", "faux")
	entries, _ := os.ReadDir(yamlDir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(yamlDir, e.Name()))
		if err == nil {
			f.Add(string(data))
		}
	}

	seeds := []string{
		"",
		"model: x\n",
		"steps: []\n",
		"steps:\n  - text: hi\n",
		"steps:\n  - tool_call:\n      name: bash\n      args: {cmd: ls}\n",
		"steps:\n  - tool_calls:\n      - name: a\n        args: {}\n      - name: b\n        raw_args: '{bad json'\n",
		"steps:\n  - on_tool_result: id1\n    then:\n      - text: ok\n",
		"steps:\n  - on_tool_results: [a, b]\n",
		"steps:\n  - error:\n      status: 500\n      type: overloaded\n      message: boom\n",
		"steps:\n  - delay: not-a-duration\n",
		"steps:\n  - disconnect_after: 20\n",
		"steps:\n  - disconnect_after: 200ms\n",
		"steps:\n  - disconnect_after: not-valid\n",
		"models:\n  a:\n    - text: hi\n  b:\n    - text: bye\n",
		"steps:\n  - tool_call:\n      name: x\n      args: {a: 1}\n      raw_args: 'also set'\n",
		"steps: {not: a list}\n",
		"model: [not, a, string]\n",
		"---\n",
		": : bad yaml :::",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, doc string) {
		s, err := parseScriptYAML(doc)
		if err != nil || s == nil {
			return
		}
		for _, steps := range s.modelScripts() {
			_, _ = flattenSteps(steps)
		}
	})
}
