package compaction

// Settings mirrors pi-agent-core's CompactionSettings
// (harness/compaction/compaction.d.ts) and budget.CompactionSettings, which
// this type is deliberately shaped to convert from at call sites.
type Settings struct {
	// Enabled gates automatic compaction decisions (ShouldCompact).
	Enabled bool
	// ReserveTokens is tokens reserved for the summarization prompt and its
	// output, and the ceiling ShouldCompact compares context usage against.
	ReserveTokens int
	// KeepRecentTokens is the approximate recent-context budget FindCutPoint
	// tries to preserve verbatim.
	KeepRecentTokens int
}

// DefaultSettings is pi's DEFAULT_COMPACTION_SETTINGS
// (harness/compaction/compaction.js).
var DefaultSettings = Settings{
	Enabled:          true,
	ReserveTokens:    16384,
	KeepRecentTokens: 20000,
}
