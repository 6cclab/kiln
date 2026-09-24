package jsonl

import "errors"

// ErrLegacyV3Unsupported is returned by Open (and Fork) when a session
// file's line-1 header is a legacy v3 header ({"type":"session",
// "version":3,...}).
//
// pi's legacy-v3.js upgrades such a file to v4 in place on first commit,
// replaying every v3 record (message, custom, custom_message,
// branch_summary, compaction, model_change, thinking_level_change,
// active_tools_change, session_info, label) into v4 entries and current
// values, importing historical usage as one adjustment row. That record
// model (compaction retained-tail reconstruction, branch-summary "root"
// sentinel translation, folding *_change records into pi.lane.config) is
// substantial and is not ported in this phase. Detection only is
// implemented: IsLegacyV3Header / ParseHeader correctly identify a v3
// file, and Open/Fork report this error rather than silently mis-handling
// it.
var ErrLegacyV3Unsupported = errors.New("legacy v3 session: upgrade not yet supported")
