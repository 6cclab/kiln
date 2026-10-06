//go:build !kiln_crashtest

package crash

// TestPoint marks a place a crash test can make panic. In a normal build
// it does nothing; a binary built with -tags kiln_crashtest panics here
// when KILN_CRASHTEST_PANIC names site (testpoint_crashtest.go).
func TestPoint(site string) {}
