package diag

import "io"

// Close reports cleanup failures when the caller has already returned its
// operation's result. Commit and write errors must be handled by the caller.
func Close(closer io.Closer) {
	if err := closer.Close(); err != nil {
		L().Warn("resource cleanup failed", "error", err)
	}
}
