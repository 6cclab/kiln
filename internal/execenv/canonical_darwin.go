//go:build darwin

package execenv

import (
	"bytes"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// CanonicalPath is the path macOS itself reports for the file p names
// (fcntl F_GETPATH on its longest existing prefix, with the missing tail
// appended). macOS opens one file under spellings that are neither
// symlinks nor case variants strings.ToLower knows: APFS case folding
// ("ſecrets" opens "secrets"), firmlinks (/System/Volumes/Data/Users/x is
// /Users/x) and /.vol/<dev>/<inode> paths. Asking the kernel for its name
// of the open file maps all of them to one spelling. p is first resolved
// with RealPath; when nothing can be opened, that is returned.
func CanonicalPath(p string) string {
	real, ok := RealPath(p)
	if !ok {
		real = filepath.Clean(p)
	}
	prefix := real
	var tail []string
	for {
		if got, ok := fGetPath(prefix); ok {
			return filepath.Join(append([]string{got}, tail...)...)
		}
		parent := filepath.Dir(prefix)
		if parent == prefix {
			return real
		}
		tail = append([]string{filepath.Base(prefix)}, tail...)
		prefix = parent
	}
}

// KernelPath is the kernel's own path for the existing file or directory
// p (F_GETPATH), without walking up for a missing one; ok is false when p
// cannot be opened. For a caller that canonicalises many paths and
// memoises parents itself.
func KernelPath(p string) (string, bool) { return fGetPath(p) }

// fGetPath asks the kernel for its path of the regular file or directory
// p. Anything else (a device, FIFO, socket or link) is never opened: ok is
// false and the caller falls back to the parent directory. The open is for
// event notification only; O_EVTONLY still needs read permission, so a
// file the process cannot read falls back to its parent too. O_NOCTTY and
// O_NONBLOCK keep the open from taking a terminal or blocking, and
// O_NOFOLLOW from following a link swapped in after the lstat.
func fGetPath(p string) (string, bool) {
	var st unix.Stat_t
	if err := unix.Lstat(p, &st); err != nil {
		return "", false
	}
	if t := st.Mode & unix.S_IFMT; t != unix.S_IFREG && t != unix.S_IFDIR {
		return "", false
	}
	fd, err := unix.Open(p, unix.O_EVTONLY|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", false
	}
	defer func() { _ = unix.Close(fd) }()
	buf := make([]byte, unix.PathMax)
	// The pointer travels as the int argument, as x/sys's own
	// FcntlFlock/FcntlFstore pass theirs.
	_, err = unix.FcntlInt(uintptr(fd), unix.F_GETPATH, int(uintptr(unsafe.Pointer(&buf[0]))))
	runtime.KeepAlive(buf)
	if err != nil {
		return "", false
	}
	if i := bytes.IndexByte(buf, 0); i >= 0 {
		buf = buf[:i]
	}
	if len(buf) == 0 || buf[0] != '/' {
		return "", false
	}
	return string(buf), true
}
