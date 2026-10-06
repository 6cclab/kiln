//go:build kiln_crashtest

package crash

import "os"

// TestPoint panics when KILN_CRASHTEST_PANIC names site. Only a binary
// built with -tags kiln_crashtest has this version; it exists for
// test/e2e's crash test, which needs a panic on a goroutine kiln started
// itself, and must never be part of a release build.
func TestPoint(site string) {
	if os.Getenv("KILN_CRASHTEST_PANIC") == site {
		panic("kiln_crashtest: injected panic at " + site)
	}
}
