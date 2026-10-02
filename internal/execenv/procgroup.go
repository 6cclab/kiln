package execenv

// Signal names the two signals callers need to send a child process tree,
// kept here (rather than exposing syscall.Signal, which Windows has no
// equivalent for) so the same call sites compile on every platform.
type Signal int

const (
	// SignalTerm asks the tree to exit, giving it a chance to clean up.
	SignalTerm Signal = iota
	// SignalKill ends the tree immediately.
	SignalKill
)
