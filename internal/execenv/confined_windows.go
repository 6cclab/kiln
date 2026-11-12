//go:build windows

package execenv

import (
	"errors"
	"os"
)

type confinedFiles struct{}

// EnableConfinedFiles fails closed because descriptor-relative path walking
// is unavailable in this implementation on Windows.
func (e *Env) EnableConfinedFiles(root string) error {
	return errors.New("isolated file access is unavailable on Windows")
}

// CloneForConfinedFiles fails closed on Windows.
func (e *Env) CloneForConfinedFiles(root string) (*Env, error) {
	return nil, errors.New("isolated file access is unavailable on Windows")
}

// ConfinedFilesEnabled reports whether Env file methods are confined.
func (e *Env) ConfinedFilesEnabled() bool { return false }

// CloseConfinedFiles releases the confined file scope.
func (e *Env) CloseConfinedFiles() error { return nil }

func (e *Env) confinedWriteFile(path string, content []byte) error {
	return errors.New("isolated file access is unavailable on Windows")
}
func (e *Env) confinedStat(path string) (FileInfo, error) {
	return FileInfo{}, errors.New("isolated file access is unavailable on Windows")
}
func (e *Env) confinedOpenRegular(path string) (*os.File, os.FileInfo, error) {
	return nil, nil, errors.New("isolated file access is unavailable on Windows")
}
