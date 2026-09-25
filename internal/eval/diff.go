package eval

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxDiffLines caps the unified diff emitted per changed text file.
const maxDiffLines = 200

// TreeDiff walks before (the pristine fixture copy) and after (the
// post-run project directory), reporting added, removed and changed
// files. Binary files (those containing a NUL byte) are reported as
// changed/added/removed without a unified diff body. It skips ".git"
// directories the way scenario.CopyFixture does.
func TreeDiff(before, after string) string {
	beforeFiles := listFiles(before)
	afterFiles := listFiles(after)

	var added, removed, changed []string
	for rel := range afterFiles {
		if _, ok := beforeFiles[rel]; !ok {
			added = append(added, rel)
		}
	}
	for rel := range beforeFiles {
		if _, ok := afterFiles[rel]; !ok {
			removed = append(removed, rel)
		}
	}
	for rel := range beforeFiles {
		if _, ok := afterFiles[rel]; !ok {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(before, rel))
		a, _ := os.ReadFile(filepath.Join(after, rel))
		if !bytes.Equal(a, b) {
			changed = append(changed, rel)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)

	if len(added) == 0 && len(removed) == 0 && len(changed) == 0 {
		return ""
	}

	var b strings.Builder
	for _, rel := range added {
		fmt.Fprintf(&b, "+++ added: %s\n", rel)
	}
	for _, rel := range removed {
		fmt.Fprintf(&b, "--- removed: %s\n", rel)
	}
	for _, rel := range changed {
		fmt.Fprintf(&b, "*** changed: %s\n", rel)
		before, _ := os.ReadFile(filepath.Join(before, rel))
		after, _ := os.ReadFile(filepath.Join(after, rel))
		b.WriteString(unifiedDiff(string(before), string(after)))
	}
	return b.String()
}

// unifiedDiff renders a minimal line-based diff (not a true LCS/Myers
// diff, just a leading-common/trailing-common trim around the changed
// middle) capped at maxDiffLines, which is plenty for a rubric prompt or
// a report.
func unifiedDiff(before, after string) string {
	if isBinary(before) || isBinary(after) {
		return "(binary content differs)\n"
	}
	bl := strings.Split(before, "\n")
	al := strings.Split(after, "\n")

	start := 0
	for start < len(bl) && start < len(al) && bl[start] == al[start] {
		start++
	}
	endB, endA := len(bl), len(al)
	for endB > start && endA > start && bl[endB-1] == al[endA-1] {
		endB--
		endA--
	}

	var out strings.Builder
	lines := 0
	for i := start; i < endB && lines < maxDiffLines; i++ {
		fmt.Fprintf(&out, "-%s\n", bl[i])
		lines++
	}
	for i := start; i < endA && lines < maxDiffLines; i++ {
		fmt.Fprintf(&out, "+%s\n", al[i])
		lines++
	}
	if lines >= maxDiffLines {
		out.WriteString("... (diff truncated)\n")
	}
	return out.String()
}

func isBinary(s string) bool {
	return strings.ContainsRune(s, 0)
}

func listFiles(root string) map[string]struct{} {
	out := map[string]struct{}{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		// The runner writes .claude/settings.json into the scratch project
		// itself (permissions, modelRoles); it is not the agent's work and
		// must not reach the judge as an "unrelated change".
		if rel == filepath.Join(".claude", "settings.json") {
			return nil
		}
		out[rel] = struct{}{}
		return nil
	})
	return out
}
