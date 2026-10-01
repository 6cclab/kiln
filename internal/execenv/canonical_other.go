//go:build !darwin

package execenv

import "path/filepath"

// CanonicalPath is RealPath off macOS: Linux has no firmlinks, /.vol or
// case-folding filesystem by default, so the real path is the canonical
// one. (canonical_darwin.go asks the kernel instead.)
func CanonicalPath(p string) string {
	return canonicalReal(p)
}

// KernelPath has nothing to ask off macOS: the real path is canonical.
func KernelPath(string) (string, bool) { return "", false }

func canonicalReal(p string) string {
	real, ok := RealPath(p)
	if !ok {
		return filepath.Clean(p)
	}
	return real
}
