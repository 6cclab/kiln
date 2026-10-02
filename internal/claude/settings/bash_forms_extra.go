package settings

import (
	"path/filepath"
	"strings"
)

// optionForms returns more texts deny and ask rules are matched against
// for one simple command (bashCmd.forms), for commands whose prefix hides
// what they do from a rule written for the plain form:
//
//   - git with global options before its subcommand: "git -C . push" and
//     "git -c a=b push" are also "git push";
//   - package runners that run another command: "npx npm publish x" is
//     also "npm publish x" (npx, bunx, pnpx, uvx, and "npm exec",
//     "pnpm dlx"/"exec", "yarn dlx"/"exec", "bun x", "pipx run",
//     "uv run", "poetry run", "bundle exec").
//
// Only deny and ask rules see these forms, so they can only make kiln
// stricter.
func optionForms(words []evalWord) []string {
	if len(words) == 0 || !words[0].literal {
		return nil
	}
	texts := make([]string, len(words))
	for i, w := range words {
		texts[i] = w.text()
	}
	name := filepath.Base(texts[0])
	var out []string
	switch name {
	case "git":
		if rest, ok := gitAfterGlobalOptions(texts[1:]); ok {
			out = append(out, "git "+strings.Join(rest, " "))
		}
	case "npx", "bunx", "pnpx", "uvx":
		if rest := skipFlags(texts[1:], map[string]bool{"-p": true, "--package": true, "--from": true, "--with": true}); len(rest) > 0 {
			out = append(out, strings.Join(rest, " "))
		}
	case "npm", "pnpm", "yarn", "bun", "pipx", "uv", "poetry", "bundle":
		sub := map[string][]string{
			"npm": {"exec", "x"}, "pnpm": {"dlx", "exec"}, "yarn": {"dlx", "exec"},
			"bun": {"x"}, "pipx": {"run"}, "uv": {"run"}, "poetry": {"run"}, "bundle": {"exec"},
		}[name]
		if len(texts) > 2 {
			for _, s := range sub {
				if texts[1] == s {
					if rest := skipFlags(texts[2:], map[string]bool{"-p": true, "--package": true, "--with": true, "--from": true}); len(rest) > 0 {
						out = append(out, strings.Join(rest, " "))
					}
				}
			}
		}
	}
	return out
}

// gitAfterGlobalOptions returns git's subcommand and its arguments once
// the global options before it are skipped; ok is false when there were
// none to skip.
func gitAfterGlobalOptions(args []string) ([]string, bool) {
	withValue := map[string]bool{"-C": true, "-c": true, "--git-dir": true, "--work-tree": true,
		"--namespace": true, "--config-env": true, "--super-prefix": true, "--exec-path": false}
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		if withValue[args[i]] {
			i++
		}
		i++
	}
	if i == 0 || i >= len(args) {
		return nil, false
	}
	return args[i:], true
}

// skipFlags drops leading flags (and the value of those in withValue).
func skipFlags(args []string, withValue map[string]bool) []string {
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		if args[i] == "--" {
			i++
			break
		}
		if withValue[args[i]] {
			i++
		}
		i++
	}
	if i >= len(args) {
		return nil
	}
	return args[i:]
}
