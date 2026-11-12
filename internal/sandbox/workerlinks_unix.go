//go:build darwin || linux

package sandbox

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Seatbelt evaluates paths, not inodes, and on Linux an inode shared with the
// parent checkout is the same file wherever it is mounted. A hardlink already in the child
// could reveal a denied file or mutate a parent file under a child name.
// Check at command preparation as well as worktree creation because files
// can be added between commands by the supervising process.
func rejectWorkerHardlinks(root string) error {
	const maxEntries = 100000
	entries := 0
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("sandbox: open worker worktree: %w", err)
	}
	return scanWorkerDir(fd, root, &entries, maxEntries)
}

// scanWorkerDir owns fd. It reads at most 256 names at a time and opens
// directories relative to the held parent descriptor without following
// symlinks. A concurrent same-user writer can still change a file after
// validation; the worker shell path is not activated on that assumption.
func scanWorkerDir(fd int, path string, entries *int, limit int) (scanErr error) {
	dir := os.NewFile(uintptr(fd), path)
	defer func() {
		if err := dir.Close(); err != nil && scanErr == nil {
			scanErr = fmt.Errorf("sandbox: close worker directory %s: %w", path, err)
		}
	}()
	for {
		names, readErr := dir.Readdirnames(256)
		if readErr != nil && readErr != io.EOF {
			return fmt.Errorf("sandbox: read worker directory %s: %w", path, readErr)
		}
		for _, name := range names {
			*entries++
			if *entries > limit {
				return fmt.Errorf("sandbox: worker worktree exceeds hardlink validation limit of %d entries", limit)
			}
			var stat unix.Stat_t
			if err := unix.Fstatat(fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return fmt.Errorf("sandbox: inspect worker file %s: %w", filepath.Join(path, name), err)
			}
			switch stat.Mode & unix.S_IFMT {
			case unix.S_IFREG:
				if stat.Nlink > 1 {
					return fmt.Errorf("sandbox: worker file %s has multiple hardlinks", filepath.Join(path, name))
				}
			case unix.S_IFDIR:
				child, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
				if err != nil {
					return fmt.Errorf("sandbox: open worker directory %s: %w", filepath.Join(path, name), err)
				}
				if err := scanWorkerDir(child, filepath.Join(path, name), entries, limit); err != nil {
					return err
				}
			}
		}
		if readErr == io.EOF {
			return nil
		}
	}
}
