package execenv

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// MaxReadFileBytes bounds a single Env.ReadFile call. A read/edit/write
// tool call can land on a multi-GB file that legitimately exists in the
// workspace (a data file, a log, a core dump, a vendored binary blob) with
// no permission prompt needed for an in-workspace read; before this cap,
// os.ReadFile allocated the whole thing, and string(bytes) /
// strings.Split doubled or tripled that footprint, before the read tool's
// own 2000-line/50KB truncation (truncate.go) ever looked at the result —
// an OOM vector for the whole process, not just the one tool call (core
// audit H1). 64 MiB is far above any real source file while keeping the
// worst case for one call in the tens, not thousands, of MB; read.go
// additionally refuses a much smaller untargeted read outright (matching
// Claude Code's own Read tool) rather than running up against this cap in
// the first place.
const MaxReadFileBytes int64 = 64 << 20

// ErrFileTooLarge is returned by ReadFile when a file is larger than its
// cap. The caller decides what to tell the model; this only carries the
// numbers needed to do that.
type ErrFileTooLarge struct {
	Path     string
	Size     int64
	MaxBytes int64
}

func (e *ErrFileTooLarge) Error() string {
	return fmt.Sprintf("%s is %s, more than the %s limit", e.Path, FormatSize(int(e.Size)), FormatSize(int(e.MaxBytes)))
}

var errNotRegularFile = errors.New("not a regular file")

// openRegular opens path for reading only if it resolves (following
// symlinks, like os.ReadFile does) to a regular file, without blocking,
// and checks the opened file again so a swap between the two checks is
// caught — mirroring internal/gitfiles's openRegular. A FIFO or device
// (ReadFile's path may be handed anything a model asks for, inside an
// already-trusted workspace) reads as an error at once, never as a hang
// or an endless read.
func openRegular(path string) (*os.File, os.FileInfo, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%s: %w", path, errNotRegularFile)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|openNonblock, 0)
	if err != nil {
		return nil, nil, err
	}
	fi2, err := f.Stat()
	if err != nil || !fi2.Mode().IsRegular() {
		f.Close()
		return nil, nil, fmt.Errorf("%s: %w", path, errNotRegularFile)
	}
	return f, fi2, nil
}

// ReadFile reads a whole regular file as bytes, refusing (*ErrFileTooLarge)
// a file over MaxReadFileBytes before allocating anything for its
// content, and never blocking on a FIFO or device. path may be relative
// to Cwd.
func (e *Env) ReadFile(path string) ([]byte, error) {
	resolved := e.AbsolutePath(path)
	f, fi, err := openRegular(resolved)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi.Size() > MaxReadFileBytes {
		return nil, &ErrFileTooLarge{Path: path, Size: fi.Size(), MaxBytes: MaxReadFileBytes}
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxReadFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > MaxReadFileBytes {
		// The file grew between the Stat above and this read finishing;
		// refuse rather than hand back a silently truncated result.
		return nil, &ErrFileTooLarge{Path: path, Size: fi.Size(), MaxBytes: MaxReadFileBytes}
	}
	return data, nil
}
