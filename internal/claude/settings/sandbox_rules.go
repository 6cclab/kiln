package settings

import "strings"

// Permission rules feed the sandbox (code.claude.com/docs/en/sandboxing,
// "Permission rules"): Edit allow rules add write access the way
// sandbox.filesystem.allowWrite does, Edit deny rules add to denyWrite,
// Read deny rules add to denyRead, and WebFetch(domain:…) allow and deny
// rules add to the network allow and deny lists.

// SandboxRulePath is the path a Read or Edit rule covers, in the form the
// sandbox profile is built from: Base, an absolute directory or file, and
// Segs, the gitignore-style segments still to match under it ("**", "*",
// "?", "[…]"). Segs empty means Base itself and everything under it.
type SandboxRulePath struct {
	Base string
	Segs []string
}

// SandboxRulePaths returns the paths permission rules add to the sandbox
// filesystem lists, each anchored the way the rule is matched for the file
// tools (pathrules.go). "!" carve-outs and bare (pathless) rules add
// nothing.
func SandboxRulePaths(p Permissions, cwd string) (allowWrite, denyWrite, denyRead []SandboxRulePath) {
	c := newMatchCtx(cwd)
	collect := func(rules []string, from []RuleSource, list ruleList, kind fileKind) []SandboxRulePath {
		var out []SandboxRulePath
		for i, rule := range rules {
			f, ok := splitFileRule(rule)
			if !ok || f.kind != kind || f.empty() {
				continue
			}
			r, ok := c.compilePathRule(f.kind, f.pattern, sourceAt(from, i), list)
			if !ok || r.neg {
				continue
			}
			// The base and segments as written, not in the case-folded
			// form a deny rule compares in: the sandbox profile decides
			// how to match case for its platform.
			segs := r.segs
			if r.loose {
				segs = r.rawSegs
			}
			out = append(out, SandboxRulePath{Base: r.base, Segs: append([]string(nil), segs...)})
		}
		return out
	}
	allowWrite = collect(p.Allow, p.AllowFrom, listAllow, kindEdit)
	denyWrite = collect(p.Deny, p.DenyFrom, listDeny, kindEdit)
	denyRead = collect(p.Deny, p.DenyFrom, listDeny, kindRead)
	return allowWrite, denyWrite, denyRead
}

// SandboxRuleDomains returns the hosts WebFetch(domain:…) allow and deny
// rules name.
func SandboxRuleDomains(p Permissions) (allow, deny []string) {
	pick := func(rules []string) []string {
		var out []string
		for _, rule := range rules {
			m := parenRule.FindStringSubmatch(strings.TrimSpace(rule))
			if m == nil || !sameTool(strings.ToLower(strings.TrimSpace(m[1])), "webfetch") {
				continue
			}
			if d, ok := strings.CutPrefix(strings.TrimSpace(m[2]), "domain:"); ok && strings.TrimSpace(d) != "" {
				out = append(out, strings.TrimSpace(d))
			}
		}
		return out
	}
	return pick(p.Allow), pick(p.Deny)
}
