package execenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The session scratchpad: a per-session directory the model writes
// temporary files to, without permission prompts, instead of /tmp or the
// project, as Claude Code's session scratchpad is used. kiln's lives under
// its own per-user temp root.
//
// MERGE NOTE: the sandbox branch makes /tmp/kiln-<uid> the sandbox's
// writable temp dir ($TMPDIR inside the sandbox). TempRoot is meant to be
// that same directory, so a sandboxed command can write the scratchpad;
// reconcile the two helpers into this one at merge.

// TempRoot is kiln's per-user temp root: $KILN_TMPDIR, else
// $CLAUDE_CODE_TMPDIR (Claude Code's override, which kiln honours as it
// reads Claude Code's configuration), else /tmp — joined with kiln-<uid>
// (just "kiln" where the system temp dir is already per-user). /tmp, not
// $TMPDIR, as Claude Code does on Unix: one well-known place per user.
func TempRoot() string {
	base := os.Getenv("KILN_TMPDIR")
	if base == "" {
		base = os.Getenv("CLAUDE_CODE_TMPDIR")
	}
	if base == "" {
		base = defaultTempBase()
	}
	return filepath.Join(base, tempDirName())
}

// ScratchpadDir is the scratchpad path for one session of the project in
// cwd: <TempRoot>/<project slug>/<sessionID>/scratchpad. It is not created.
func ScratchpadDir(cwd, sessionID string) string {
	return filepath.Join(TempRoot(), projectSlug(cwd), safeName(sessionID), "scratchpad")
}

// EnsureScratchpad creates the session's scratchpad, every level 0700, and
// returns its real path. It refuses — returning an error, so the session
// runs without one — a temp root that is a symlink, not a directory, not
// owned by this user, or open to others: in a shared /tmp someone else could
// have created it first to read or redirect what the model writes.
func EnsureScratchpad(cwd, sessionID string) (string, error) {
	root := TempRoot()
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		return "", err
	}
	if err := os.Mkdir(root, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	if err := checkPrivateDir(root); err != nil {
		return "", err
	}
	dir := ScratchpadDir(cwd, sessionID)
	rel, _ := filepath.Rel(root, dir)
	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		if err := os.Mkdir(cur, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
		if err := checkPrivateDir(cur); err != nil {
			return "", err
		}
	}
	real, ok := RealPath(dir)
	if !ok {
		return "", fmt.Errorf("scratchpad %s: cannot resolve", dir)
	}
	return real, nil
}

// projectSlug names a project directory in one path component.
func projectSlug(cwd string) string {
	return safeName(filepath.Clean(cwd))
}

// safeName keeps letters, digits, '.', '_' and '-' and turns everything
// else into '-'; never "", ".", or "..".
func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	if strings.Trim(out, "-") == "" {
		return "session"
	}
	return out
}
