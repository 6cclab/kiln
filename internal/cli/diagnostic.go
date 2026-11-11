package cli

import (
	"fmt"
	"github.com/andrepato/harness/internal/diag"
	"io"
)

// writeDiagnostic reports an already-failed operation; if stderr is unavailable
// the run log retains that diagnostic failure.
func writeDiagnostic(out io.Writer, values ...any) {
	if _, err := fmt.Fprintln(out, values...); err != nil {
		diag.L().Warn("diagnostic output failed", "error", err)
	}
}
