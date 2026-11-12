//go:build !linux

package sandbox

import (
	"errors"

	"github.com/andrepato/harness/internal/execenv"
)

// nsWatch exists only on Linux (nswatch_linux.go); elsewhere nothing owns a
// PID namespace.
type nsWatch struct{}

var _ nsWatch // Manager.nsWatches names the type on every platform

var errNoNS = errors.New("sandbox: PID namespace teardown needs Linux")

func (m *Manager) probeNSWatch() error { return errNoNS }
func (m *Manager) shutdownWatches() error {
	if len(m.nsWatches) > 0 { // never filled off Linux
		return errNoNS
	}
	return nil
}
func (m *Manager) attachNSWatch(*execenv.Wrapped) error { return errNoNS }
