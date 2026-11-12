//go:build !windows

package execenv

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// confinedFiles holds an open directory descriptor, so renaming the root
// cannot redirect later file operations.
type confinedFiles struct {
	mu   sync.Mutex
	root *os.File
}

// EnableConfinedFiles confines file tool operations to root. Every path
// component is opened relative to a directory descriptor without following
// symlinks. The caller must close this scope after its workers stop.
func (e *Env) EnableConfinedFiles(root string) error {
	if e.confined != nil {
		return errors.New("confined files already enabled")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	fd, err := unix.Open(abs, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open confined root: %w", err)
	}
	e.Cwd = abs
	e.confined = &confinedFiles{root: os.NewFile(uintptr(fd), abs)}
	return nil
}

// CloneForConfinedFiles copies the Env configuration and pins a new worker
// root. It does not share or close a parent's confined root descriptor.
func (e *Env) CloneForConfinedFiles(root string) (*Env, error) {
	if e == nil {
		return nil, errors.New("nil execution environment")
	}
	clone := *e
	clone.confined = nil
	if err := clone.EnableConfinedFiles(root); err != nil {
		return nil, err
	}
	return &clone, nil
}

// ConfinedFilesEnabled reports whether Env file methods are confined.
func (e *Env) ConfinedFilesEnabled() bool {
	if e == nil || e.confined == nil {
		return false
	}
	e.confined.mu.Lock()
	defer e.confined.mu.Unlock()
	return e.confined.root != nil
}

// CloseConfinedFiles releases the worker's pinned root directory.
func (e *Env) CloseConfinedFiles() error {
	if e.confined == nil {
		return nil
	}
	c := e.confined
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.root == nil {
		return nil
	}
	err := c.root.Close()
	c.root = nil
	return err
}

func (e *Env) confinedParts(path string) ([]string, error) {
	abs := e.AbsolutePath(path)
	rel, err := filepath.Rel(e.Cwd, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == "." {
		return nil, fmt.Errorf("path outside confined worktree: %s", path)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return nil, fmt.Errorf("confined path component denied: %s", part)
		}
	}
	return parts, nil
}

// confinedParent walks descriptors, optionally creating missing directories.
// Its caller owns the returned descriptor.
func (e *Env) confinedParent(path string, create bool) (int, string, error) {
	parts, err := e.confinedParts(path)
	if err != nil {
		return -1, "", err
	}
	e.confined.mu.Lock()
	if e.confined.root == nil {
		e.confined.mu.Unlock()
		return -1, "", errors.New("confined files closed")
	}
	// F_DUPFD_CLOEXEC sets the flag in the same operation that creates the
	// descriptor. A separate CloseOnExec call could race a shell fork.
	fd, err := unix.FcntlInt(e.confined.root.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	e.confined.mu.Unlock()
	if err != nil {
		return -1, "", err
	}
	for _, part := range parts[:len(parts)-1] {
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr == unix.ENOENT && create {
			if mkErr := unix.Mkdirat(fd, part, 0o755); mkErr != nil && mkErr != unix.EEXIST {
				_ = unix.Close(fd)
				return -1, "", mkErr
			}
			next, openErr = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		_ = unix.Close(fd)
		if openErr != nil {
			return -1, "", fmt.Errorf("confined directory %s: %w", part, openErr)
		}
		fd = next
	}
	return fd, parts[len(parts)-1], nil
}

func (e *Env) confinedOpenRegular(path string) (*os.File, os.FileInfo, error) {
	fd, name, err := e.confinedParent(path, false)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = unix.Close(fd) }()
	child, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	f := os.NewFile(uintptr(child), path)
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("%s: %w", path, errNotRegularFile)
	}
	var st unix.Stat_t
	if err := unix.Fstat(child, &st); err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if st.Nlink > 1 {
		_ = f.Close()
		return nil, nil, fmt.Errorf("confined target has multiple hard links: %s", path)
	}
	return f, fi, nil
}

func (e *Env) confinedStat(path string) (FileInfo, error) {
	fd, name, err := e.confinedParent(path, false)
	if err != nil {
		return FileInfo{}, err
	}
	defer func() { _ = unix.Close(fd) }()
	var st unix.Stat_t
	if err = unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return FileInfo{}, err
	}
	kind := KindFile
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		kind = KindSymlink
	case unix.S_IFDIR:
		kind = KindDirectory
	}
	return FileInfo{Name: name, Path: e.AbsolutePath(path), Kind: kind, Size: st.Size,
		MtimeMs: time.Unix(st.Mtim.Sec, st.Mtim.Nsec).UnixMilli()}, nil
}

func (e *Env) confinedWriteFile(path string, content []byte) error {
	fd, name, err := e.confinedParent(path, true)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	// Never truncate an existing inode: a hard link could refer outside the
	// worktree. Write a private sibling and replace the directory entry.
	var st unix.Stat_t
	mode := uint32(0o644)
	existing := false
	if err = unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		existing = true
		if st.Mode&unix.S_IFMT != unix.S_IFREG {
			return fmt.Errorf("confined target is not a regular file: %s", path)
		}
		if st.Nlink > 1 {
			return fmt.Errorf("confined target has multiple hard links: %s", path)
		}
		mode = uint32(st.Mode & 0o777)
	} else if err != unix.ENOENT {
		return err
	}
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		return err
	}
	tmp := ".kiln-" + hex.EncodeToString(token[:])
	createMode := mode
	if existing {
		createMode = 0o600
	}
	child, err := unix.Openat(fd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, createMode)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(child), tmp)
	_, writeErr := f.Write(content)
	if writeErr == nil && existing {
		writeErr = f.Chmod(os.FileMode(mode))
	}
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		cleanupErr := unix.Unlinkat(fd, tmp, 0)
		return errors.Join(writeErr, closeErr, cleanupErr)
	}
	// A concurrent symlink swap can only change which entry is replaced;
	// renameat never follows the destination.
	if err = unix.Renameat(fd, tmp, fd, name); err != nil {
		cleanupErr := unix.Unlinkat(fd, tmp, 0)
		return errors.Join(err, cleanupErr)
	}
	return nil
}
