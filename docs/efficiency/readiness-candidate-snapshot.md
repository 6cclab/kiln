# Readiness candidate: preserved parent snapshot

Captured 2026-11-12 04:58 UTC from the dirty source checkout as a byte-for-byte baseline. The child's product source is the copied snapshot, unedited.

## Provenance

- Source checkout: `/Users/andrepato/projects/harness-kiln` at `5a7150f554fa0294de41084b999f560a7a307cec`.
- Child worktree: `/Users/andrepato/orca/workspaces/harness-kiln/readiness-candidate` on branch `anpato/readiness-candidate`.
- Preserved source commit: `966fb18a2ee8a2c8cb7de4afa6da9cc97055c404` (`Preserve in-flight efficiency work as readiness baseline`).
- Source status SHA-256: `59842c7fb68dab5b9ede9a0a103ca4d6453bde4d88bbc5eb3bbc5647520654b9`. The status included 18 modified tracked files, four captured untracked Go files, and one excluded untracked operational brief.
- Captured `git diff HEAD --binary --full-index` SHA-256: `4ba3e6c570d68d03f07fc08b271a13fac835b1e128de9769f1b5c57681375c7c` (43978 bytes). The child tracked diff matched this digest before staging.
- Binary-safe source archive SHA-256: `8ea66b5c3c41a608fc8a8c72166e5181f91fa39049c728a0c67ce0b89f45fcb1`. Archive entries retained source permission bits.
- Capture manifest SHA-256: `b816bb8cbe50c96d4aaed9798131f739f14b80ba62bd54cd015ff6d84d74a591`.
- Scratch artifacts: `/private/tmp/kiln-readiness-capture-skvuybmg/parent.patch`, `source.tar`, `manifest.json`.

The source HEAD, NUL-delimited status, binary diff digest, and each selected file’s SHA-256 and mode matched before capture, after archive creation, after child extraction, and after both test gates. The child copy matched all 22 source file digests and modes. The source checkout was only read; its status and file bytes were unchanged.

## Captured files

| Path | SHA-256 | Mode |
|---|---|---|
| `internal/agent/dispatch.go` | `aaaecc9024ee25a45bd257a46ddb6ef53eb05a9e568742ff3a2180a3aa605aae` | `0o644` |
| `internal/agent/worker_batch.go` | `071ff86e758ee1a5a6674084a175b7e45380f6fe34ead269d884836b40745219` | `0o644` |
| `internal/agent/worker_batch_test.go` | `3c0d3bbb88aa7b830bd18b1f6ce316ff8e766adf4b5e3151491b55e755186286` | `0o644` |
| `internal/claude/commands/commands.go` | `830910a50d74eda4e58787e019b96a5c3f3b7b564c2ff9c2fc9bd64654e61db8` | `0o644` |
| `internal/claude/commands/commands_test.go` | `41afa93189ead0c2af16ce57859f322d817b4c5307b6cfc1a18349122e76c561` | `0o644` |
| `internal/claude/settings/efficiency_sources.go` | `22d469e88e0bfed2a557c23a37f407cbde82e538bf3a043ccd93180e6509b1f6` | `0o644` |
| `internal/claude/settings/orchestration.go` | `8aed2bf4247662fe0b6938de7adb2d5249bb5d7c37ee481939f8411b9599fca8` | `0o644` |
| `internal/claude/settings/orchestration_test.go` | `241a3688c29042669ea839a0d2a191521b1c8c7fadc74b990c6563ab7773e171` | `0o644` |
| `internal/cli/command_discovery.go` | `e041fba89ea3338328965930b6abd3d4036c168b54602928a2a334f1436d05d2` | `0o644` |
| `internal/cli/command_discovery_test.go` | `bb3a28e16862be534a5995c09126e0222a27b41524bc214cdaed10d29481dd8e` | `0o644` |
| `internal/cli/discovery.go` | `1cb080cd707dfe04faafb1c65feec1b8541eca2832a38e9861ee5da010e975c1` | `0o644` |
| `internal/cli/learning.go` | `648bc7db1af29a208c7c82796537fdce82ff5537033a3a5bd7e63ef625d01008` | `0o644` |
| `internal/cli/learning_outcomes.go` | `e0d53cb897a4d81385d68c11de0bf2005967b8ee0fd1f9ee7e9ca1d3a55d355b` | `0o644` |
| `internal/cli/learning_outcomes_test.go` | `39e4fe18a824936e8cb78e8a528adcf3bfc1756751fa67b63e0be832ca6a3e6e` | `0o644` |
| `internal/learning/outcomes.go` | `7520f4ccaedd0eb4d3c97ba083c05b6d81589bd286f9cfc1500be99841676c4e` | `0o644` |
| `internal/learning/proposals.go` | `95e68d222312825ef5afa11b37d84f0652ba73b92434e1a79bbf3d15e074a73c` | `0o644` |
| `internal/learning/proposals_test.go` | `782ea332297add096b8bdf12377e9516c6aaf2c7ad7663b5462fff8fcdf8ec75` | `0o644` |
| `internal/memory/store.go` | `d329e07c554d6e9b31532b4c00f261d12cb9377e8c47da7bb750c6fece0d3bfe` | `0o644` |
| `internal/tools/capability_search.go` | `b1b7aa1a53ef59884d147e7e29e8c81a1dcbc946c1872c5fedeed4f1915852f5` | `0o644` |
| `internal/tools/capability_search_test.go` | `a35f93d0ac9d928ea962ea4bc12af729f42e4dc57501e611f76a33829baccc1e` | `0o644` |
| `internal/tools/skill.go` | `1d0c9a3fcf00f1c4b546b628e20e8951bd0b3f185a81133477beb85ddd4475a5` | `0o644` |
| `internal/tools/skill_test.go` | `2c5e8c1b94cd328cebd672da497fbe6d84bd575d290217289cd47ca671573d01` | `0o644` |

All four initially untracked `.go` files were included. `scripts/orca/efficiency-coordinator.md` was excluded as an operational brief; ignored secrets and generated artifacts were not copied.

## Verification

- `git diff --check`: passed before staging.
- `make check`: passed with the repository-pinned staticcheck 2026.2.2, all unit tests, and e2e-tag vet. The first sandboxed attempts were blocked by Go cache access and network resolution; the exact command passed with a scratch `GOCACHE` and escalated network access.
- A second `make check` passed immediately before the report commit; log `/private/tmp/kiln-readiness-capture-skvuybmg/make-check-report.log`, SHA-256 `e5d9acb2ba3fabdd1b5c82d602ae2a4343029f70f419616fcdc6df553a63da29`.
- `make e2e`: passed, `test/e2e` in 231.142s. The first sandboxed attempt was blocked from opening loopback sockets; the exact command passed with loopback access.
- No regression test was added or reverted by this worker because this is a preservation snapshot, not a new behavior change. Tests copied from the source were run as part of the gates.

### Passing `make check` log

Original log: `/private/tmp/kiln-readiness-capture-skvuybmg/make-check-exact.log`; SHA-256 `7f4bb2c08446d5af6f7f136c056134f0a89c750db937fdf6748fb6df09fb1960`.

```text
==> gofmt
==> go vet
go vet ./...
==> staticcheck
go run honnef.co/go/tools/cmd/staticcheck@2026.2.2 ./...
==> go test
go test ./...
ok  	github.com/andrepato/harness/cmd/covercheck	0.086s
?   	github.com/andrepato/harness/cmd/faux	[no test files]
ok  	github.com/andrepato/harness/cmd/kiln	2.478s
ok  	github.com/andrepato/harness/cmd/kiln-drive	1.301s
ok  	github.com/andrepato/harness/cmd/mcpfixture	0.156s
?   	github.com/andrepato/harness/cmd/vtreplay	[no test files]
?   	github.com/andrepato/harness/eval/fixtures/vendor-rule/vendor	[no test files]
?   	github.com/andrepato/harness/eval/fixtures/versioned-app	[no test files]
ok  	github.com/andrepato/harness/internal/agent	37.401s
ok  	github.com/andrepato/harness/internal/auth	0.106s
ok  	github.com/andrepato/harness/internal/auth/login	0.187s
ok  	github.com/andrepato/harness/internal/auth/oauth	14.282s
ok  	github.com/andrepato/harness/internal/automode	0.237s
ok  	github.com/andrepato/harness/internal/budget	0.123s
ok  	github.com/andrepato/harness/internal/claude/agents	0.149s
ok  	github.com/andrepato/harness/internal/claude/commands	0.201s
ok  	github.com/andrepato/harness/internal/claude/hooks	5.504s
ok  	github.com/andrepato/harness/internal/claude/keybindings	0.088s
ok  	github.com/andrepato/harness/internal/claude/memory	0.585s
ok  	github.com/andrepato/harness/internal/claude/paths	0.064s
ok  	github.com/andrepato/harness/internal/claude/permission	0.527s
ok  	github.com/andrepato/harness/internal/claude/plugins	0.217s
ok  	github.com/andrepato/harness/internal/claude/settings	7.779s
ok  	github.com/andrepato/harness/internal/claude/skills	0.125s
ok  	github.com/andrepato/harness/internal/claude/statusline	0.194s
ok  	github.com/andrepato/harness/internal/claude/trust	0.115s
ok  	github.com/andrepato/harness/internal/claude/writesettings	0.092s
ok  	github.com/andrepato/harness/internal/cli	4.420s
ok  	github.com/andrepato/harness/internal/commands	0.227s
ok  	github.com/andrepato/harness/internal/compaction	0.237s
ok  	github.com/andrepato/harness/internal/crash	0.720s
ok  	github.com/andrepato/harness/internal/diag	0.074s
ok  	github.com/andrepato/harness/internal/eval	1.965s
ok  	github.com/andrepato/harness/internal/execenv	10.372s
ok  	github.com/andrepato/harness/internal/gitfiles	0.199s
ok  	github.com/andrepato/harness/internal/harness	13.395s
ok  	github.com/andrepato/harness/internal/imgpath	0.397s
ok  	github.com/andrepato/harness/internal/learning	0.280s
ok  	github.com/andrepato/harness/internal/mcp	10.412s
ok  	github.com/andrepato/harness/internal/memory	0.147s
ok  	github.com/andrepato/harness/internal/mods	1.800s
ok  	github.com/andrepato/harness/internal/money	0.061s
ok  	github.com/andrepato/harness/internal/msg	0.069s
ok  	github.com/andrepato/harness/internal/plural	0.060s
ok  	github.com/andrepato/harness/internal/provider	0.089s
ok  	github.com/andrepato/harness/internal/provider/api	0.280s
ok  	github.com/andrepato/harness/internal/provider/builtin	0.117s
ok  	github.com/andrepato/harness/internal/provider/catalog	0.080s
ok  	github.com/andrepato/harness/internal/provider/faux	0.092s
ok  	github.com/andrepato/harness/internal/provider/ollama	2.929s
ok  	github.com/andrepato/harness/internal/router	0.094s
ok  	github.com/andrepato/harness/internal/sandbox	44.757s
ok  	github.com/andrepato/harness/internal/search	0.160s
ok  	github.com/andrepato/harness/internal/session	0.068s
ok  	github.com/andrepato/harness/internal/session/jsonl	0.136s
ok  	github.com/andrepato/harness/internal/testkit/faux	6.585s
?   	github.com/andrepato/harness/internal/testkit/fauxtest	[no test files]
ok  	github.com/andrepato/harness/internal/testkit/hygiene	0.150s
ok  	github.com/andrepato/harness/internal/testkit/mcpfixture	2.985s
ok  	github.com/andrepato/harness/internal/testkit/scenario	0.093s
ok  	github.com/andrepato/harness/internal/testkit/screen	5.902s
ok  	github.com/andrepato/harness/internal/testkit/sessiondata	0.102s
ok  	github.com/andrepato/harness/internal/testkit/stubtui	0.074s
?   	github.com/andrepato/harness/internal/testkit/stubtui/cmd/stubtui	[no test files]
ok  	github.com/andrepato/harness/internal/tool	0.067s
ok  	github.com/andrepato/harness/internal/tools	6.322s
ok  	github.com/andrepato/harness/internal/tui	4.064s
ok  	github.com/andrepato/harness/internal/tui/editor	0.136s
?   	github.com/andrepato/harness/qa/fixtures/incident-mcp	[no test files]
==> go vet (e2e build tag)
go vet -tags e2e ./test/e2e/...
```

### Passing `make e2e` log

Original log: `/private/tmp/kiln-readiness-capture-skvuybmg/make-e2e-exact.log`; SHA-256 `d10e57d3972f6917670464cf97faac0d4bd86a7cb878353dbdaa63414cb252d1`.

```text
go test -tags e2e ./test/e2e/... -count=1 -timeout 10m
ok  	github.com/andrepato/harness/test/e2e	231.142s
```

## Scope and next steps

The snapshot preserves in-flight work on worker integration, custom command discovery, and learning proposals. `IntegrateWorkerResults` is present as an explicit batch helper; this snapshot added no call site. The coordinator can route integration and broader readiness review from this stable baseline. No other branch was cherry-picked, and no push, PR, or merge was made.
