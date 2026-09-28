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
	// Indexed lists rules listed by description instead of loaded in
	// full, because the budget ran out.
	Indexed         []string
	EstimatedTokens int
	// OverBudget is set when the instruction files alone exceed the
	// budget; they load in full anyway.
	OverBudget bool
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
// Instruction files (CLAUDE.md: ~/.claude/CLAUDE.md, <cwd>/CLAUDE.md and
// <cwd>/.claude/CLAUDE.md, with @imports resolved) always load in full:
// they are what the user wrote for every session, and small. Rules
// (.claude/rules/*.md) load in full while the budget allows, the most
// specific (project) first; the rest are listed in an index, one line per
// rule with its path and description, and the model is told to read a rule
// before doing work its description covers. Nothing is silently left out:
// a small-context model keeps a pointer to every rule at a fraction of the
// cost.
func LoadMemory(cwd string, budgetTokens int) Assembled {
	var instructions, rules []File
	for _, root := range paths.ClaudeRoots(cwd) {
		var candidates []string
		if root.Scope == paths.ScopeUser {
			candidates = []string{filepath.Join(root.Dir, paths.CLAUDEMD)}
		} else {
			// The project file sits beside .claude or inside it; Claude
			// Code reads both.
			candidates = []string{filepath.Join(cwd, paths.CLAUDEMD), filepath.Join(cwd, ".claude", paths.CLAUDEMD)}
		}
		for _, path := range candidates {
			if raw, err := os.ReadFile(path); err == nil {
				content := resolveImports(string(raw), path, map[string]bool{path: true}, 0)
				instructions = append(instructions, File{Path: path, Scope: root.Scope, Content: content})
			}
		}
		for _, rule := range listRules(filepath.Join(root.Dir, "rules")) {
			if raw, err := os.ReadFile(rule); err == nil {
				content := resolveImports(string(raw), rule, map[string]bool{rule: true}, 0)
				rules = append(rules, File{Path: rule, Scope: root.Scope, Content: content})
			}
		}
	}

	used := 0
	for _, f := range instructions {
		used += estimate(f.Content)
	}

	// Rules found later are more specific; they claim the budget first.
	full := make([]bool, len(rules))
	var indexed []File
	for i := len(rules) - 1; i >= 0; i-- {
		cost := estimate(rules[i].Content)
		if used+cost <= budgetTokens {
			full[i] = true
			used += cost
		}
	}
	kept := append([]File{}, instructions...)
	for i, f := range rules {
		if full[i] {
			kept = append(kept, f)
		} else {
			indexed = append(indexed, f)
		}
	}

	parts := make([]string, 0, len(kept)+1)
	for _, f := range kept {
		parts = append(parts, fmt.Sprintf("<memory path=%q scope=%q>\n%s\n</memory>", f.Path, f.Scope, strings.TrimSpace(f.Content)))
	}
	var indexedPaths []string
	if len(indexed) > 0 {
		lines := []string{"<memory-index>", "These rules are not loaded, to save context. Before doing work a rule's description covers, read its file with the read tool and follow it."}
		for _, f := range indexed {
			lines = append(lines, fmt.Sprintf("- %s: %s", f.Path, ruleSummary(f.Content)))
			indexedPaths = append(indexedPaths, f.Path)
		}
		lines = append(lines, "</memory-index>")
		index := strings.Join(lines, "\n")
		used += estimate(index)
		parts = append(parts, index)
	}

	return Assembled{
		Text:            strings.Join(parts, "\n\n"),
		Files:           kept,
		Indexed:         indexedPaths,
		EstimatedTokens: used,
		OverBudget:      used > budgetTokens,
	}
}

var (
	descriptionRe = regexp.MustCompile(`(?m)^description:\s*(.+?)\s*$`)
	headingRe     = regexp.MustCompile(`(?m)^#+\s+(.+?)\s*$`)
)

// ruleSummary is a rule's one-line description for the index: its
// frontmatter description, else its first heading, else its first line.
func ruleSummary(content string) string {
	if fm, ok := strings.CutPrefix(strings.TrimLeft(content, "\n"), "---\n"); ok {
		if end := strings.Index(fm, "\n---"); end >= 0 {
			if m := descriptionRe.FindStringSubmatch(fm[:end]); m != nil {
				return strings.Trim(m[1], `"'`)
			}
		}
	}
	if m := headingRe.FindStringSubmatch(content); m != nil {
		return m[1]
	}
	first, _, _ := strings.Cut(strings.TrimSpace(content), "\n")
	return first
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

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("could not create %s: %w", filepath.Dir(path), err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", fmt.Errorf("could not save note to %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString("\n" + strings.TrimSpace(note) + "\n"); err != nil {
		return "", err
	}
	return path, nil
}
