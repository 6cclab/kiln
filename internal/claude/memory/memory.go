package memory

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/andrepato/harness/internal/claude/paths"
)

const maxImportDepth = 5

// homeDir returns the user's home directory, or "" if it cannot be
// determined.
func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// listRules returns markdown files in a .claude/rules directory, sorted
// for stable ordering. A missing directory is the normal case.
func listRules(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

// File is one loaded memory file, after import resolution.
type File struct {
	Path    string
	Scope   paths.Scope
	Content string
}

// Assembled is the result of LoadMemory.
type Assembled struct {
	Text  string
	Files []File
	// Dropped lists files dropped because the budget was exhausted.
	Dropped         []string
	EstimatedTokens int
}

// estimate is a rough token estimate, only used for budgeting, never
// reported as exact.
func estimate(text string) int {
	return int(math.Ceil(float64(len(text)) / 4))
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(homeDir(), path[2:])
	}
	return path
}

var importLineRe = regexp.MustCompile(`^@(\S+)\s*$`)

// resolveImports resolves @path imports recursively. seen guards against
// cycles: two files importing each other would otherwise recurse forever,
// and a self-import is an easy typo.
func resolveImports(content, fromFile string, seen map[string]bool, depth int) string {
	if depth >= maxImportDepth {
		return content
	}

	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))

	for _, line := range lines {
		// Only a line that is *just* an import counts. An inline "@foo" in
		// prose (an email address, a handle) must not trigger a file read.
		m := importLineRe.FindStringSubmatch(line)
		if m == nil {
			out = append(out, line)
			continue
		}

		target := expandHome(m[1])
		var path string
		if filepath.IsAbs(target) {
			path = target
		} else {
			path = filepath.Join(filepath.Dir(fromFile), target)
		}
		if seen[path] {
			out = append(out, fmt.Sprintf("<!-- skipped circular import: %s -->", m[1]))
			continue
		}
		seen[path] = true

		data, err := os.ReadFile(path)
		if err != nil {
			// A broken import is worth surfacing in-band: the
			// instructions the author expected are absent, and silence
			// would hide that.
			out = append(out, fmt.Sprintf("<!-- missing import: %s -->", m[1]))
			continue
		}
		out = append(out, resolveImports(string(data), path, seen, depth+1))
	}
	return strings.Join(out, "\n")
}

// LoadMemory loads and assembles memory within a token budget.
//
// Order is least- to most-specific (user, then project), and the budget
// is spent in reverse: project memory is the most situational and
// survives, while broad personal preferences are dropped first if
// something must go.
func LoadMemory(cwd string, budgetTokens int) Assembled {
	var found []File

	for _, root := range paths.ClaudeRoots(cwd) {
		// ~/.claude/CLAUDE.md for user scope; <project>/CLAUDE.md at the
		// repo root for project scope - note the project file sits beside
		// .claude, not inside it.
		var path string
		if root.Scope == paths.ScopeUser {
			path = filepath.Join(root.Dir, paths.CLAUDEMD)
		} else {
			path = filepath.Join(cwd, paths.CLAUDEMD)
		}
		if raw, err := os.ReadFile(path); err == nil {
			content := resolveImports(string(raw), path, map[string]bool{path: true}, 0)
			found = append(found, File{Path: path, Scope: root.Scope, Content: content})
		}

		// .claude/rules/*.md are loaded as memory too, without needing an
		// explicit import.
		for _, rule := range listRules(filepath.Join(root.Dir, "rules")) {
			if raw, err := os.ReadFile(rule); err == nil {
				content := resolveImports(string(raw), rule, map[string]bool{rule: true}, 0)
				found = append(found, File{Path: rule, Scope: root.Scope, Content: content})
			}
		}
	}

	var kept []File
	var dropped []string
	used := 0

	// Reverse so the most specific scope claims budget first.
	for i := len(found) - 1; i >= 0; i-- {
		f := found[i]
		cost := estimate(f.Content)
		if used+cost > budgetTokens {
			dropped = append(dropped, f.Path)
			continue
		}
		kept = append([]File{f}, kept...)
		used += cost
	}

	parts := make([]string, len(kept))
	for i, f := range kept {
		parts[i] = fmt.Sprintf("<memory path=%q scope=%q>\n%s\n</memory>", f.Path, f.Scope, strings.TrimSpace(f.Content))
	}

	return Assembled{
		Text:            strings.Join(parts, "\n\n"),
		Files:           kept,
		Dropped:         dropped,
		EstimatedTokens: used,
	}
}

// AddMemory appends a note to <cwd>/CLAUDE.md if it exists, else
// ~/.claude/CLAUDE.md, returning the path it wrote to.
//
// Ported from harness/src/tui/input-modes.ts's addMemory (the "#note"
// input mode), not from memory.ts, which has no such function. Writing to
// the project file first matters: most notes worth keeping are about the
// code in front of you, not about you.
func AddMemory(note, cwd string) (string, error) {
	projectPath := filepath.Join(cwd, paths.CLAUDEMD)
	path := filepath.Join(homeDir(), ".claude", paths.CLAUDEMD)
	if _, err := os.Stat(projectPath); err == nil {
		path = projectPath
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString("\n" + strings.TrimSpace(note) + "\n"); err != nil {
		return "", err
	}
	return path, nil
}
