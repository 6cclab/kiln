// Package execenv is the execution environment tools run against: a
// filesystem rooted at a working directory plus shell command execution.
//
// It mirrors pi-agent-core's NodeExecutionEnv
// (node_modules/@earendil-works/pi-agent-core/dist/harness/env/nodejs.js)
// and the FileSystem/Shell interfaces it implements
// (dist/harness/types.d.ts). Where pi returns a Result union so callers
// never see a thrown error, this package uses ordinary Go error returns;
// the operations and their observable behavior (path resolution, shell
// selection, output capture/truncation, process-group kill on
// cancellation) are the same.
package execenv
