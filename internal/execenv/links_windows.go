//go:build windows

package execenv

import (
	"os"
	"syscall"
)

// SharedFile reports an existing non-directory at p with more than one
// hard link (NTFS links): the same file is also reachable by another name,
// so where p sits says nothing about what writing it changes. A missing
// path or a directory is not shared; a file whose link count cannot be
// read is.
func SharedFile(p string) bool {
	fi, err := os.Lstat(p)
	if err != nil || fi.IsDir() {
		return false
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return false // the link itself, not its target, is what Lstat saw
	}
	name, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return true
	}
	h, err := syscall.CreateFile(name, 0, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return true
	}
	defer syscall.CloseHandle(h)
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &info); err != nil {
		return true
	}
	return info.NumberOfLinks > 1
}
