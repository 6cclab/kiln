package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

const goPartialDiscovery = "go-partial-v1"

type discoveredVerificationInputs struct {
	files, missing, directories []string
}

// discoverGoVerificationInputs recognizes only one literal local go test
// package command. The resulting set is deliberately partial: imports,
// replacements, toolchain, environment, and files outside the selected tree
// are not discovered.
func discoverGoVerificationInputs(cwd, command string, allowRead func(string) bool) (discoveredVerificationInputs, error) {
	var out discoveredVerificationInputs
	if cwd == "" || allowRead == nil || len(command) > maxVerificationCommandBytes {
		return out, fmt.Errorf("discovery unavailable")
	}
	parsed, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil || len(parsed.Stmts) != 1 {
		return out, fmt.Errorf("unsupported verification command")
	}
	stmt := parsed.Stmts[0]
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || stmt.Background || stmt.Coprocess || stmt.Negated || stmt.Disown || len(stmt.Redirs) != 0 || len(call.Assigns) != 0 || len(call.Args) != 3 {
		return out, fmt.Errorf("unsupported verification command")
	}
	words := make([]string, 3)
	for i, word := range call.Args {
		if len(word.Parts) != 1 {
			return out, fmt.Errorf("nonliteral verification command")
		}
		lit, ok := word.Parts[0].(*syntax.Lit)
		if !ok {
			return out, fmt.Errorf("nonliteral verification command")
		}
		words[i] = lit.Value
	}
	if words[0] != "go" || words[1] != "test" || !strings.HasPrefix(words[2], "./") {
		return out, fmt.Errorf("unsupported verification command")
	}
	path := strings.TrimSuffix(strings.TrimPrefix(words[2], "./"), "/...")
	if path == "" || path == "." || strings.Contains(path, "...") || !validRelativeVerificationPath(path) {
		return out, fmt.Errorf("unsupported verification package path")
	}
	// No shell metacharacters or Go package patterns are admitted. The source
	// spelling must be canonical so the directory snapshotted is the one run.
	if words[2] != "./"+path && words[2] != "./"+path+"/..." {
		return out, fmt.Errorf("unsupported verification package path")
	}
	for _, c := range path {
		if c == '*' || c == '?' || c == '[' || c == ']' || c == '{' || c == '}' || c == '\\' || c == ':' {
			return out, fmt.Errorf("unsupported verification package path")
		}
	}
	out.directories = []string{path}
	for _, name := range []string{"go.mod", "go.sum", "go.work", "go.work.sum"} {
		if !allowRead(name) {
			return discoveredVerificationInputs{}, fmt.Errorf("verification metadata unavailable")
		}
		info, err := os.Lstat(filepath.Join(cwd, name))
		if os.IsNotExist(err) && name != "go.mod" {
			out.missing = append(out.missing, name)
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return discoveredVerificationInputs{}, fmt.Errorf("verification metadata unavailable")
		}
		out.files = append(out.files, name)
	}
	return out, nil
}

func verificationMissingFiles(cwd string, paths []string, allowRead func(string) bool) bool {
	for _, path := range paths {
		if !validRelativeVerificationPath(path) || allowRead == nil || !allowRead(path) {
			return false
		}
		_, err := os.Lstat(filepath.Join(cwd, path))
		if !os.IsNotExist(err) {
			return false
		}
	}
	return true
}

func appendUniqueVerificationPaths(existing, additional []string) []string {
	seen := make(map[string]bool, len(existing)+len(additional))
	for _, path := range existing {
		seen[path] = true
	}
	for _, path := range additional {
		if !seen[path] {
			existing = append(existing, path)
			seen[path] = true
		}
	}
	return existing
}

func validGoDiscoveryEvidence(proof VerificationEvidence) bool {
	if len(proof.Directories) == 0 {
		return false
	}
	seen := make(map[string]bool)
	for _, file := range proof.Files {
		seen[file.Path] = true
	}
	if !seen["go.mod"] {
		return false
	}
	for _, path := range proof.MissingFiles {
		if path != "go.sum" && path != "go.work" && path != "go.work.sum" {
			return false
		}
		seen[path] = true
	}
	for _, path := range []string{"go.sum", "go.work", "go.work.sum"} {
		if !seen[path] {
			return false
		}
	}
	return true
}
