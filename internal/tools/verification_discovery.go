package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// go-partial-v2 adds the recorded environment values. go-partial-v1 records
// (saved before that field existed) stay readable and compare no environment.
const goPartialDiscoveryV1 = "go-partial-v1"
const goPartialDiscovery = "go-partial-v2"

const maxGoVerificationEnvBytes = 4096

// goVerificationEnvNames are the process environment values that change which
// files a go test build selects or how it compiles them.
var goVerificationEnvNames = []string{"CGO_ENABLED", "GOARCH", "GOEXPERIMENT", "GOFLAGS", "GOOS", "GOTOOLCHAIN", "GOWORK"}

func isGoPartialDiscovery(discovery string) bool {
	return discovery == goPartialDiscovery || discovery == goPartialDiscoveryV1
}

type discoveredVerificationInputs struct {
	files, missing, directories []string
	env                         map[string]string
}

func currentGoVerificationEnv() (map[string]string, bool) {
	var env map[string]string
	for _, name := range goVerificationEnvNames {
		if value, ok := os.LookupEnv(name); ok {
			if len(value) > maxGoVerificationEnvBytes {
				return nil, false
			}
			if env == nil {
				env = make(map[string]string)
			}
			env[name] = value
		}
	}
	return env, true
}

// changedGoVerificationEnv names the recorded environment values that differ
// from the current process environment. v1 records carry none.
func changedGoVerificationEnv(proof VerificationEvidence) []string {
	if proof.Discovery != goPartialDiscovery {
		return nil
	}
	current, ok := currentGoVerificationEnv()
	var changed []string
	for _, name := range goVerificationEnvNames {
		old, hadOld := proof.Env[name]
		now, hasNow := current[name]
		if !ok || hadOld != hasNow || old != now {
			changed = append(changed, name)
		}
	}
	return changed
}

func validGoVerificationEnv(proof VerificationEvidence) bool {
	if proof.Discovery != goPartialDiscovery {
		return len(proof.Env) == 0
	}
	for name, value := range proof.Env {
		if !slices.Contains(goVerificationEnvNames, name) || len(value) > maxGoVerificationEnvBytes {
			return false
		}
	}
	return true
}

// discoverGoVerificationInputs recognizes only one literal local go test
// package command. The resulting set is deliberately partial; unsupported
// build selection and execution inputs are described by verificationScope.
func discoverGoVerificationInputs(cwd, command string, allowRead func(string) bool) (discoveredVerificationInputs, error) {
	var out discoveredVerificationInputs
	if cwd == "" || allowRead == nil || len(command) > maxVerificationCommandBytes {
		return out, fmt.Errorf("discovery unavailable")
	}
	parsed, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil || len(parsed.Stmts) != 1 {
		return out, fmt.Errorf("compound commands or shell syntax unsupported")
	}
	stmt := parsed.Stmts[0]
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || stmt.Background || stmt.Coprocess || stmt.Negated || stmt.Disown || len(stmt.Redirs) != 0 || len(call.Assigns) != 0 || len(call.Args) != 3 {
		return out, fmt.Errorf("compound commands, redirects or assignments unsupported")
	}
	words := make([]string, 3)
	for i, word := range call.Args {
		if len(word.Parts) != 1 {
			return out, fmt.Errorf("shell expansions or quoting unsupported")
		}
		lit, ok := word.Parts[0].(*syntax.Lit)
		if !ok {
			return out, fmt.Errorf("shell expansions or quoting unsupported")
		}
		words[i] = lit.Value
	}
	if words[0] != "go" || words[1] != "test" || !strings.HasPrefix(words[2], "./") {
		return out, fmt.Errorf("flags or unsupported verification command")
	}
	path := strings.TrimSuffix(strings.TrimPrefix(words[2], "./"), "/...")
	if words[2] == "./..." {
		path = "."
	}
	if path == "" || strings.Contains(path, "...") || path != "." && !validRelativeVerificationPath(path) {
		return out, fmt.Errorf("unsupported verification package path")
	}
	// No shell metacharacters or Go package patterns are admitted. The source
	// spelling must be canonical so the directory snapshotted is the one run.
	if path != "." && words[2] != "./"+path && words[2] != "./"+path+"/..." || path == "." && words[2] != "./..." {
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
	env, ok := currentGoVerificationEnv()
	if !ok {
		return discoveredVerificationInputs{}, fmt.Errorf("environment value too large")
	}
	out.env = env
	more, err := discoverGoLocalDependencies(cwd, path, strings.HasSuffix(words[2], "/..."), allowRead)
	if err != nil {
		return discoveredVerificationInputs{}, err
	}
	out.directories = appendUniqueVerificationPaths(out.directories, more)
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
	if len(proof.Directories) == 0 || !validGoVerificationEnv(proof) {
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
