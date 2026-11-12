# Worker confinement and verification discovery

This tranche strengthens existing isolated editing workers and adds an opt-in
way to capture inputs for a narrow family of Go checks. It does not enable
isolated shell workers or establish generated-code quality parity.

## Confined file tools

On macOS and Linux, each isolated worker pins its own directory descriptor.
Read, edit and write operations traverse path components relative to open
directories with symlink following disabled. Git metadata paths and multiply
linked files are refused. Writes replace directory entries rather than
truncating existing inodes, preserving an existing file's permissions.
Directory descriptors are closed on exec so they cannot leak into subprocesses.

Nested workers get separate file scopes. Closing one cannot close its parent,
and operations after closure fail instead of falling back to ordinary file
access. Worker commands stop before file-scope and worktree cleanup. Windows
isolated editing fails explicitly until an equivalent implementation exists;
normal sessions retain their existing filesystem behavior.

## Worker sandbox foundation

`sandbox.NewWorker` builds a separate mandatory macOS Seatbelt manager from
resolved settings. It writes only to the child checkout and its private temp
directory, protects shared Git metadata, rebases inherited read/write rules,
and offers no unsandboxed, proxy or Unix socket exception. Linux is refused:
the current bubblewrap backend cannot comprehensively block host Unix sockets.

The factory is not wired into dispatch. Hardlink validation before construction
and commands is bounded to 100,000 entries with bounded directory reads, but a
concurrent external writer could change the tree after validation. Live probes
test parent and child hardlink creation, denied reads, parent/Git writes, and
TCP/Unix sockets on the local macOS profile. These probes do not establish a
general containment guarantee. Shell activation needs further integration and
validation; existing isolated workers remain file-only.

## Partial Go verification inputs

With task state enabled, Bash accepts `verificationDiscover: "go"` for one
literal `go test ./path` or `go test ./path/...` invocation. It snapshots the
selected package tree and root Go module/workspace metadata without running
another command. Shell expansions, flags, compound commands and whole-repository
`./...` discovery are unsupported. Explicit input declarations can supplement
the discovered set under the existing shared limits.

Optional metadata absence is saved too: adding `go.work`, for example, changes
verification status and produces a rerun suggestion. Discovery provenance and
absence snapshots survive branch restoration. Unsupported, denied or oversized
input sets produce no partial proof and do not suppress the requested command.

The set deliberately omits imports outside the selected tree, replacements,
toolchain, environment and ancestor workspace files. Evidence and recovery label
it as partial; unchanged evidence never warrants skipping verification.

## Validation

Regression tests exercise symlink/hardlink reads, concurrent ancestor replacement,
descriptor lifetime and inheritance, permission preservation, nested scope
ownership, metadata creation during and after checks, and restored evidence.
The real CLI discovery test fails against the previous implementation because
no evidence is saved. Disabling the confined read branch makes the outside-file
regressions fail. No paid comparison was run.
