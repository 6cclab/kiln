package tools

import (
	"os"
	"path/filepath"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/tool"
)

// refuseVolPath refuses a path under macOS's /.vol (as written, or after
// resolving symlinks), which opens a file by device and inode number: a
// spelling no permission rule can name, so the file tools do not open it.
func refuseVolPath(absolutePath, shown string) (tool.Result, bool) {
	real, _ := execenv.RealPath(absolutePath)
	if execenv.IsVolPath(absolutePath) || execenv.IsVolPath(real) {
		return tool.Errorf("Refusing %s: /.vol paths open files by inode number. Use the file's ordinary path.", shown), true
	}
	return tool.Result{}, false
}

// refuseSymlink is Claude Code's rule for the Edit and Write tools: when
// the path asked for is itself a symlink, refuse and name the link's
// target, so the model edits the file it actually means and the permission
// check sees that path ("Writes through a symlink",
// https://code.claude.com/docs/en/permissions). A symlinked directory
// further up the path is not refused; the gate judges the resolved path.
func refuseSymlink(env *execenv.Env, absolutePath, shown string) (tool.Result, bool) {
	info, err := env.Stat(absolutePath)
	if err != nil || info.Kind != execenv.KindSymlink {
		return tool.Result{}, false
	}
	target, err := os.Readlink(absolutePath)
	if err != nil {
		return tool.Errorf("Refusing to write %s: it is a symlink, and its target could not be read.", shown), true
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(absolutePath), target)
	}
	target = filepath.Clean(target)
	return tool.Errorf("Refusing to write %s: it is a symlink to %s. Use %s as the path if that is the file you mean.", shown, target, target), true
}

// normalizeToolPath is execenv.NormalizeToolPath. It lives there so the
// permission gate resolves a path argument exactly as the tools do.
func normalizeToolPath(path string) string {
	return execenv.NormalizeToolPath(path)
}

// resolveToolPath mirrors path-utils.js's resolveToolPath.
func resolveToolPath(env *execenv.Env, path string) string {
	return env.AbsolutePath(normalizeToolPath(path))
}

// resolveReadToolPath mirrors path-utils.js's resolveReadToolPath: try the
// literal path first, then each of execenv.ReadPathVariants (filenames the
// model normalized away: a narrow no-break space before AM/PM, a curly
// apostrophe), falling back to the literal resolved path if none exist so
// callers get pi's ordinary "not found" error instead of a silent
// substitution. The permission gate judges the same variant list, so a
// deny rule covers whichever one this opens.
func resolveReadToolPath(env *execenv.Env, path string) string {
	resolved := resolveToolPath(env, path)
	for _, variant := range execenv.ReadPathVariants(resolved) {
		if ok, err := env.Exists(variant); err == nil && ok {
			return variant
		}
	}
	return resolved
}
