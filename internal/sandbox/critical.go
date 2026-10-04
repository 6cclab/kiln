package sandbox

import (
	"path"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// criticalRemoval reports an rm or rmdir that targets a critical path as
// Claude Code's permission-modes docs define one ("Critical paths"): the
// filesystem root, a top-level directory, the home directory, the working
// directory or one of its parents, a glob directly under one of the
// workspace roots, and a target built from a variable. The sandbox would
// let such a removal of the workspace through, so it is not auto-allowed;
// it goes through the regular permission flow. A line kiln cannot parse
// counts as critical.
func criticalRemoval(command, cwd, home string, roots []string) bool {
	return CriticalRemoval(command, cwd, home, roots)
}

// CriticalRemoval is criticalRemoval for a caller with no sandbox: the
// permission gate checks it whether or not the sandbox is on, since no
// allow rule or hook "allow" approves such a removal (Claude Code's
// permission-modes docs, "Critical paths").
func CriticalRemoval(command, cwd, home string, roots []string) bool {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return strings.Contains(command, "rm")
	}
	critical := false
	syntax.Walk(f, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if !ok || critical || len(call.Args) == 0 {
			return !critical
		}
		name, ok := literalWord(call.Args[0])
		if !ok || (path.Base(name) != "rm" && path.Base(name) != "rmdir") {
			return true
		}
		endOpts := false
		for _, w := range call.Args[1:] {
			lit, isLit := literalWord(w)
			if !isLit {
				// A glob or a variable: what it removes is decided at run
				// time.
				raw := wordSource(w)
				if strings.HasPrefix(raw, "-") && !endOpts {
					continue
				}
				if criticalGlob(raw, cwd, home, roots) || strings.Contains(raw, "$") {
					critical = true
					return false
				}
				continue
			}
			if lit == "--" {
				endOpts = true
				continue
			}
			if strings.HasPrefix(lit, "-") && !endOpts {
				continue
			}
			if criticalTarget(resolveTarget(lit, cwd, home), cwd, home) {
				critical = true
				return false
			}
		}
		return true
	})
	return critical
}

func wordSource(w *syntax.Word) string {
	var b strings.Builder
	_ = syntax.NewPrinter().Print(&b, w)
	return b.String()
}

func resolveTarget(p, cwd, home string) string {
	switch {
	case p == "~" || strings.HasPrefix(p, "~/"):
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	case !filepath.IsAbs(p):
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p)
}

func criticalTarget(p, cwd, home string) bool {
	if p == "/" || filepath.Dir(p) == "/" || p == filepath.Clean(home) {
		return true
	}
	for d := filepath.Clean(cwd); ; d = filepath.Dir(d) {
		if p == d {
			return true
		}
		if d == filepath.Dir(d) {
			return false
		}
	}
}

// criticalGlob: a glob directly under a critical path or a workspace root
// ("rm -rf *", "rm -rf ~/*", "rm -rf /*").
func criticalGlob(raw, cwd, home string, roots []string) bool {
	raw = strings.Trim(raw, `"'`)
	dir, base := filepath.Split(raw)
	if !strings.ContainsAny(base, "*?[") {
		return false
	}
	d := resolveTarget(strings.TrimSuffix(dir, "/"), cwd, home)
	if dir == "" {
		d = filepath.Clean(cwd)
	}
	if criticalTarget(d, cwd, home) {
		return true
	}
	for _, r := range roots {
		if d == filepath.Clean(r) {
			return true
		}
	}
	return false
}
